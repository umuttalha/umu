package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB
var sqlCache = make(map[int]string)
var cacheMu sync.Mutex

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(0)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbPath := os.Getenv("UMUT_DB_PATH")
	if dbPath == "" {
		dbPath = "/workspace/data.db"
	}

	os.MkdirAll(filepath.Dir(dbPath), 0755)

	var err error
	db, err = sql.Open("sqlite", dbPath+"?_journal=WAL&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=-16000&_foreign_keys=ON")
	if err != nil {
		log.Fatalf("sqlite-server: failed to open database: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("sqlite-server: WAL mode warning: %v", err)
	}

	log.Printf("sqlite-server: listening on :%s (db=%s)", port, dbPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v2", handleV2Base)
	mux.HandleFunc("/v2/pipeline", handleV2Pipeline)
	mux.HandleFunc("/", handleV1)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	srv.SetKeepAlivesEnabled(false)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("sqlite-server: %v", err)
	}
}

// ===== Health & Version =====

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleV2Base(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// ===== V2 Pipeline (Hrana protocol) =====

type v2Stmt struct {
	SQL       string `json:"sql"`
	SqlID     *int   `json:"sql_id"`
	Args      []any  `json:"args"`
	NamedArgs []any  `json:"named_args"`
	WantRows  bool   `json:"want_rows"`
}

type v2Request struct {
	Type  string      `json:"type"`
	SQL   string      `json:"sql"`
	Stmt  v2Stmt      `json:"stmt"`
	Batch *v2BatchBody `json:"batch"`
	SqlID int         `json:"sql_id"`
}

type v2BatchBody struct {
	Steps []v2BatchStep `json:"steps"`
}

type v2BatchStep struct {
	Stmt      *v2Stmt      `json:"stmt"`
	Condition *v2BatchCond `json:"condition"`
}

type v2BatchCond struct {
	Type string       `json:"type"`
	Step *int         `json:"step"`
	Cond *v2BatchCond `json:"cond"`
}

type v2PipelineRequest struct {
	Baton    *string     `json:"baton"`
	Requests []v2Request `json:"requests"`
}

type v2PipelineResponse struct {
	Baton   *string        `json:"baton"`
	BaseURL *string        `json:"base_url"`
	Results []v2ResultItem `json:"results"`
}

type v2ResultItem struct {
	Type     string      `json:"type"`
	Response *v2Response `json:"response,omitempty"`
	Error    *v2ResultErr `json:"error,omitempty"`
}

type v2Response struct {
	Type   string    `json:"type"`
	Result *v2Result `json:"result,omitempty"`
}

type v2Result struct {
	Cols             []v2Col        `json:"cols"`
	Rows             [][]any        `json:"rows"`
	AffectedRowCount uint64         `json:"affected_row_count"`
	LastInsertRowid  *string        `json:"last_insert_rowid"`
	StepResults      []*v2StepResult `json:"step_results,omitempty"`
	StepErrors       []*v2StepError  `json:"step_errors,omitempty"`
}

type v2StepResult struct {
	Cols             []v2Col `json:"cols"`
	Rows             [][]any `json:"rows"`
	AffectedRowCount uint64  `json:"affected_row_count"`
	LastInsertRowid  *string `json:"last_insert_rowid"`
}

type v2StepError struct {
	Step  int         `json:"step"`
	Error v2ResultErr `json:"error"`
}

type v2Col struct {
	Name string `json:"name"`
}

type v2ResultErr struct {
	Message string `json:"message"`
}

func handleV2Pipeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV2Error(w, "only POST supported")
		return
	}

	var req v2PipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV2Error(w, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Requests) == 0 {
		writeV2Error(w, "no requests")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := &v2PipelineResponse{}

	for _, rv := range req.Requests {
		switch rv.Type {
		case "execute":
			result, err := executeStmt(rv.Stmt)
			if err != nil {
				resp.Results = append(resp.Results, v2ResultItem{
					Type: "error", Error: &v2ResultErr{Message: err.Error()},
				})
			} else {
				resp.Results = append(resp.Results, v2ResultItem{
					Type: "ok", Response: &v2Response{Type: "execute", Result: result},
				})
			}
		case "batch":
			if rv.Batch == nil {
				resp.Results = append(resp.Results, v2ResultItem{
					Type: "error", Error: &v2ResultErr{Message: "missing batch field"},
				})
			} else {
				result, err := executeBatch(rv.Batch.Steps)
				if err != nil {
					resp.Results = append(resp.Results, v2ResultItem{
						Type: "error", Error: &v2ResultErr{Message: err.Error()},
					})
				} else {
					resp.Results = append(resp.Results, v2ResultItem{
						Type: "ok", Response: &v2Response{Type: "batch", Result: result},
					})
				}
			}
		case "store_sql":
			if rv.SQL != "" {
				cacheMu.Lock()
				sqlCache[rv.SqlID] = rv.SQL
				cacheMu.Unlock()
			}
			resp.Results = append(resp.Results, v2ResultItem{
				Type: "ok", Response: &v2Response{Type: "store_sql"},
			})
		case "close":
			resp.Results = append(resp.Results, v2ResultItem{
				Type: "ok", Response: &v2Response{Type: "close"},
			})
		default:
			resp.Results = append(resp.Results, v2ResultItem{
				Type: "error", Error: &v2ResultErr{Message: "unknown request type: " + rv.Type},
			})
		}
	}

	json.NewEncoder(w).Encode(resp)
}

func writeV2Error(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(&v2PipelineResponse{
		Results: []v2ResultItem{
			{Type: "error", Error: &v2ResultErr{Message: msg}},
		},
	})
}

// ===== V2 Statement Execution =====

func executeStmt(stmt v2Stmt) (*v2Result, error) {
	params := untypeArgs(stmt.Args)
	return runSQL(stmt.SQL, params)
}

func executeBatch(steps []v2BatchStep) (*v2Result, error) {
	ctx := context.Background()

	stepOk := make([]bool, len(steps))
	stepResults := make([]*v2StepResult, len(steps))
	stepErrors := make([]*v2StepError, len(steps))

	for i, step := range steps {
		if step.Condition != nil && !evalCondition(step.Condition, stepOk) {
			continue
		}

		if step.Stmt == nil {
			continue
		}

		sqlStr := step.Stmt.SQL
		if sqlStr == "" && step.Stmt.SqlID != nil {
			cacheMu.Lock()
			sqlStr = sqlCache[*step.Stmt.SqlID]
			cacheMu.Unlock()
		}
		if sqlStr == "" {
			continue
		}

		params := untypeArgs(step.Stmt.Args)
		normalized := strings.TrimSpace(strings.ToUpper(sqlStr))

		if strings.HasPrefix(normalized, "SELECT") || strings.HasPrefix(normalized, "PRAGMA") || strings.HasPrefix(normalized, "EXPLAIN") {
			rows, err := db.QueryContext(ctx, sqlStr, params...)
			if err != nil {
				stepErrors[i] = &v2StepError{Step: i, Error: v2ResultErr{Message: err.Error()}}
				break
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				stepErrors[i] = &v2StepError{Step: i, Error: v2ResultErr{Message: err.Error()}}
				break
			}
			cols := make([]v2Col, len(columns))
			for ci, c := range columns {
				cols[ci] = v2Col{Name: c}
			}
			var resultRows [][]any
			for rows.Next() {
				values := make([]any, len(columns))
				ptrs := make([]any, len(columns))
				for ci := range values {
					ptrs[ci] = &values[ci]
				}
				if err := rows.Scan(ptrs...); err != nil {
					rows.Close()
					stepErrors[i] = &v2StepError{Step: i, Error: v2ResultErr{Message: err.Error()}}
					break
				}
				for ci, v := range values {
					values[ci] = normalizeV2Value(v)
				}
				resultRows = append(resultRows, values)
			}
			rows.Close()
			stepResults[i] = &v2StepResult{Cols: cols, Rows: resultRows}
			stepOk[i] = true
		} else {
			res, err := db.ExecContext(ctx, sqlStr, params...)
			if err != nil {
				stepErrors[i] = &v2StepError{Step: i, Error: v2ResultErr{Message: err.Error()}}
				break
			}
			sr := &v2StepResult{Cols: []v2Col{}, Rows: [][]any{}}
			if res != nil {
				a, _ := res.RowsAffected()
				sr.AffectedRowCount = uint64(a)
				lid, _ := res.LastInsertId()
				if lid > 0 {
					s := fmt.Sprintf("%d", lid)
					sr.LastInsertRowid = &s
				}
			}
			stepResults[i] = sr
			stepOk[i] = true
		}
	}

	return &v2Result{
		StepResults: stepResults,
		StepErrors:  stepErrors,
	}, nil
}

func evalCondition(c *v2BatchCond, stepOk []bool) bool {
	switch c.Type {
	case "ok":
		if c.Step != nil && *c.Step >= 0 && *c.Step < len(stepOk) {
			return stepOk[*c.Step]
		}
		return true
	case "error":
		if c.Step != nil && *c.Step >= 0 && *c.Step < len(stepOk) {
			return !stepOk[*c.Step]
		}
		return false
	case "not":
		if c.Cond != nil {
			return !evalCondition(c.Cond, stepOk)
		}
		return false
	case "and", "or":
		return true
	default:
		return true
	}
}

func runSQL(sqlStr string, params []any) (*v2Result, error) {
	ctx := context.Background()
	normalized := strings.TrimSpace(strings.ToUpper(sqlStr))

	if strings.HasPrefix(normalized, "SELECT") || strings.HasPrefix(normalized, "PRAGMA") || strings.HasPrefix(normalized, "EXPLAIN") {
		rows, err := db.QueryContext(ctx, sqlStr, params...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}

		cols := make([]v2Col, len(columns))
		for i, c := range columns {
			cols[i] = v2Col{Name: c}
		}

		var resultRows [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			for i, v := range values {
				values[i] = normalizeV2Value(v)
			}
			resultRows = append(resultRows, values)
		}

		return &v2Result{Cols: cols, Rows: resultRows}, nil
	}

	res, err := db.ExecContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}

	affected, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()

	rd := &v2Result{
		Cols:             []v2Col{},
		Rows:             [][]any{},
		AffectedRowCount: uint64(affected),
	}

	if lastID > 0 {
		lid := fmt.Sprintf("%d", lastID)
		rd.LastInsertRowid = &lid
	}

	return rd, nil
}

// ===== Value conversion =====

func untypeArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	params := make([]any, len(args))
	for i, a := range args {
		params[i] = untypeValue(a)
	}
	return params
}

func untypeValue(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	typ, _ := m["type"].(string)
	val := m["value"]

	switch typ {
	case "null":
		return nil
	case "integer":
		if s, ok := val.(string); ok {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return s
			}
			return n
		}
		return val
	case "float":
		return val
	case "text":
		return val
	case "blob":
		if b64, ok := m["base64"].(string); ok {
			d, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return b64
			}
			return d
		}
		return val
	default:
		return v
	}
}

func normalizeV2Value(v any) any {
	if v == nil {
		return map[string]any{"type": "null"}
	}
	switch val := v.(type) {
	case int64:
		return map[string]any{"type": "integer", "value": fmt.Sprintf("%d", val)}
	case float64:
		return map[string]any{"type": "float", "value": val}
	case string:
		return map[string]any{"type": "text", "value": val}
	case []byte:
		return map[string]any{"type": "text", "value": string(val)}
	case bool:
		if val {
			return map[string]any{"type": "integer", "value": "1"}
		}
		return map[string]any{"type": "integer", "value": "0"}
	case time.Time:
		return map[string]any{"type": "text", "value": val.Format(time.RFC3339)}
	default:
		return map[string]any{"type": "text", "value": fmt.Sprintf("%v", val)}
	}
}

func normalizeValue(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return v
	}
}

// ===== V1 Legacy API (POST /) =====

type statement struct {
	Q      string          `json:"q"`
	Params json.RawMessage `json:"params"`
}

type batchRequest struct {
	Statements []json.RawMessage `json:"statements"`
}

type resultEntry struct {
	Results *resultData `json:"results,omitempty"`
	Error   *errorData  `json:"error,omitempty"`
}

type resultData struct {
	Columns     []string `json:"columns"`
	Rows        [][]any  `json:"rows"`
	RowsWritten *int64   `json:"rows_written,omitempty"`
}

type errorData struct {
	Message string `json:"message"`
}

func handleV1(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV1Error(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Statements) == 0 {
		writeV1Error(w, http.StatusBadRequest, "no statements")
		return
	}

	w.Header().Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, "connection: "+err.Error())
		return
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, "begin tx: "+err.Error())
		return
	}

	var results []resultEntry
	hasError := false
	for _, raw := range req.Statements {
		sqlStr, params, err := parseV1Statement(raw)
		if err != nil {
			tx.Rollback()
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		entry := executeV1(tx, sqlStr, params)
		if entry.Error != nil {
			tx.Rollback()
			results = append(results, entry)
			hasError = true
			break
		}
		results = append(results, entry)
	}
	if !hasError {
		if err := tx.Commit(); err != nil {
			results = []resultEntry{{Error: &errorData{Message: err.Error()}}}
		}
	}
	json.NewEncoder(w).Encode(results)
}

func parseV1Statement(raw json.RawMessage) (string, []any, error) {
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("empty statement")
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s, nil, nil
	}
	var stmt statement
	if err := json.Unmarshal(raw, &stmt); err != nil {
		return "", nil, err
	}
	if stmt.Q == "" {
		return "", nil, fmt.Errorf("missing q field")
	}
	if len(stmt.Params) == 0 || string(stmt.Params) == "null" {
		return stmt.Q, nil, nil
	}

	if stmt.Params[0] == '[' {
		var positional []any
		json.Unmarshal(stmt.Params, &positional)
		return stmt.Q, positional, nil
	}

	if stmt.Params[0] == '{' {
		var named map[string]any
		if err := json.Unmarshal(stmt.Params, &named); err != nil {
			return "", nil, err
		}
		sqlStr, params, err := rewriteNamed(stmt.Q, named)
		if err != nil {
			return "", nil, err
		}
		return sqlStr, params, nil
	}

	return "", nil, fmt.Errorf("invalid params")
}

func executeV1(tx *sql.Tx, sqlStr string, params []any) resultEntry {
	ctx := context.Background()
	normalized := strings.TrimSpace(strings.ToUpper(sqlStr))

	if strings.HasPrefix(normalized, "SELECT") || strings.HasPrefix(normalized, "PRAGMA") || strings.HasPrefix(normalized, "EXPLAIN") {
		rows, err := tx.QueryContext(ctx, sqlStr, params...)
		if err != nil {
			return resultEntry{Error: &errorData{Message: err.Error()}}
		}
		defer rows.Close()
		columns, _ := rows.Columns()
		var result [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return resultEntry{Error: &errorData{Message: err.Error()}}
			}
			for i, v := range values {
				values[i] = normalizeValue(v)
			}
			result = append(result, values)
		}
		return resultEntry{Results: &resultData{Columns: columns, Rows: result}}
	}

	res, err := tx.ExecContext(ctx, sqlStr, params...)
	if err != nil {
		return resultEntry{Error: &errorData{Message: err.Error()}}
	}
	aff, _ := res.RowsAffected()
	return resultEntry{Results: &resultData{Columns: []string{}, Rows: [][]any{}, RowsWritten: &aff}}
}

func rewriteNamed(sqlStr string, named map[string]any) (string, []any, error) {
	names := findPlaceholders(sqlStr)
	if len(names) == 0 {
		return sqlStr, nil, nil
	}

	var ordered []string
	seen := map[string]bool{}
	for _, n := range names {
		key := stripPrefix(n)
		if !seen[key] {
			seen[key] = true
			ordered = append(ordered, key)
		}
	}

	var params []any
	for _, key := range ordered {
		found := false
		for k, v := range named {
			if stripPrefix(k) == key {
				params = append(params, v)
				found = true
				break
			}
		}
		if !found {
			return "", nil, fmt.Errorf("missing named argument %q", key)
		}
	}

	return rewriteSQL(sqlStr, names), params, nil
}

func findPlaceholders(sql string) []string {
	var result []string
	s := sql
	pos := 0
	for pos < len(s) {
		idx := strings.IndexAny(s[pos:], "?@$:")
		if idx < 0 {
			break
		}
		pos += idx
		ch := s[pos]
		pos++
		if ch == '?' {
			start := pos - 1
			if pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
				for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
					pos++
				}
			}
			result = append(result, s[start:pos])
			continue
		}
		start := pos - 1
		if ch == ':' && pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
				pos++
			}
			result = append(result, s[start:pos])
			continue
		}
		if pos < len(s) && isIdent(s[pos]) {
			for pos < len(s) && isIdentBody(s[pos]) {
				pos++
			}
			result = append(result, s[start:pos])
		}
	}
	return result
}

func rewriteSQL(sql string, names []string) string {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	var b strings.Builder
	b.Grow(len(sql))
	pos := 0
	for pos < len(sql) {
		idx := strings.IndexAny(sql[pos:], "?@$:")
		if idx < 0 {
			b.WriteString(sql[pos:])
			break
		}
		b.WriteString(sql[pos : pos+idx])
		pos += idx
		ch := sql[pos]
		start := pos
		pos++
		if ch == '?' {
			end := pos
			for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
				end++
			}
			raw := sql[start:end]
			if set[raw] {
				b.WriteByte('?')
				pos = end
			} else {
				b.WriteString(sql[start:pos])
			}
			continue
		}
		if ch == ':' {
			if pos < len(sql) && sql[pos] >= '0' && sql[pos] <= '9' {
				end := pos
				for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
					end++
				}
				if set[sql[start:end]] {
					b.WriteByte('?')
					pos = end
				} else {
					b.WriteString(sql[start:pos])
				}
				continue
			}
		}
		if pos < len(sql) && isIdent(sql[pos]) {
			for pos < len(sql) && isIdentBody(sql[pos]) {
				pos++
			}
			raw := sql[start:pos]
			isNamed := raw[0] == ':' || raw[0] == '@' || raw[0] == '$'
			hasLetter := false
			for c := 1; c < len(raw); c++ {
				if (raw[c] >= 'a' && raw[c] <= 'z') || (raw[c] >= 'A' && raw[c] <= 'Z') {
					hasLetter = true
					break
				}
			}
			if isNamed && hasLetter && set[raw] {
				b.WriteByte('?')
			} else {
				b.WriteString(raw)
			}
		}
	}
	return b.String()
}

func stripPrefix(s string) string {
	if len(s) > 0 && (s[0] == ':' || s[0] == '@' || s[0] == '$') {
		return s[1:]
	}
	return s
}

func isIdent(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentBody(c byte) bool {
	return isIdent(c) || (c >= '0' && c <= '9')
}

func writeV1Error(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode([]resultEntry{{Error: &errorData{Message: msg}}})
}

// unused import guard
var _ = io.EOF
