package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

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

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("sqlite-server: WAL mode warning: %v", err)
	}

	log.Printf("sqlite-server: listening on :%s (db=%s)", port, dbPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/", handleQuery)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("sqlite-server: %v", err)
	}
}

type queryRequest struct {
	Query  string   `json:"query"`
	Params []any    `json:"params"`
	Many   []query  `json:"many"`
	Tx     *txReq   `json:"tx"`
}

type query struct {
	Query  string `json:"query"`
	Params []any  `json:"params"`
}

type txReq struct {
	Queries []query `json:"queries"`
}

type queryResponse struct {
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	LastInsertID int64    `json:"lastInsertId"`
	RowsAffected int64    `json:"rowsAffected"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if req.Tx != nil && len(req.Tx.Queries) > 0 {
		handleTransaction(w, req.Tx.Queries)
		return
	}

	if len(req.Many) > 0 {
		handleBatch(w, req.Many)
		return
	}

	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	result := executeQuery(req.Query, req.Params)
	json.NewEncoder(w).Encode(result)
}

func handleTransaction(w http.ResponseWriter, queries []query) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to acquire connection: "+err.Error())
		return
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
		return
	}

	var results []queryResponse
	for _, q := range queries {
		result := executeQueryTx(tx, q.Query, q.Params)
		if len(result.Columns) == 0 && len(result.Rows) > 0 {
			if errVal, ok := result.Rows[0][0].(string); ok {
				tx.Rollback()
				writeError(w, http.StatusBadRequest, errVal)
				return
			}
		}
		results = append(results, result)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit: "+err.Error())
		return
	}

	json.NewEncoder(w).Encode(results)
}

func handleBatch(w http.ResponseWriter, queries []query) {
	var results []queryResponse
	for _, q := range queries {
		result := executeQuery(q.Query, q.Params)
		results = append(results, result)
	}
	json.NewEncoder(w).Encode(results)
}

func executeQuery(queryStr string, params []any) queryResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	normalized := strings.TrimSpace(strings.ToUpper(queryStr))
	if strings.HasPrefix(normalized, "SELECT") || strings.HasPrefix(normalized, "PRAGMA") || strings.HasPrefix(normalized, "EXPLAIN") {
		return executeRead(ctx, queryStr, params)
	}
	return executeWrite(ctx, queryStr, params)
}

func executeRead(ctx context.Context, queryStr string, params []any) queryResponse {
	rows, err := db.QueryContext(ctx, queryStr, toDriverValues(params)...)
	if err != nil {
		return queryResponse{Rows: [][]any{{err.Error()}}}
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return queryResponse{Rows: [][]any{{err.Error()}}}
	}

	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return queryResponse{Rows: [][]any{{err.Error()}}}
		}
		for i, v := range values {
			values[i] = normalizeValue(v)
		}
		result = append(result, values)
	}

	return queryResponse{Columns: columns, Rows: result}
}

func executeWrite(ctx context.Context, queryStr string, params []any) queryResponse {
	result, err := db.ExecContext(ctx, queryStr, toDriverValues(params)...)
	if err != nil {
		return queryResponse{Rows: [][]any{{err.Error()}}}
	}

	lastID, _ := result.LastInsertId()
	affected, _ := result.RowsAffected()

	return queryResponse{LastInsertID: lastID, RowsAffected: affected}
}

func executeQueryTx(tx *sql.Tx, queryStr string, params []any) queryResponse {
	ctx := context.Background()

	normalized := strings.TrimSpace(strings.ToUpper(queryStr))
	if strings.HasPrefix(normalized, "SELECT") || strings.HasPrefix(normalized, "PRAGMA") || strings.HasPrefix(normalized, "EXPLAIN") {
		rows, err := tx.QueryContext(ctx, queryStr, toDriverValues(params)...)
		if err != nil {
			return queryResponse{Rows: [][]any{{err.Error()}}}
		}
		defer rows.Close()

		columns, _ := rows.Columns()
		var result [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			valuePtrs := make([]any, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}
			rows.Scan(valuePtrs...)
			for i, v := range values {
				values[i] = normalizeValue(v)
			}
			result = append(result, values)
		}
		return queryResponse{Columns: columns, Rows: result}
	}

	result, err := tx.ExecContext(ctx, queryStr, toDriverValues(params)...)
	if err != nil {
		return queryResponse{Rows: [][]any{{err.Error()}}}
	}
	lastID, _ := result.LastInsertId()
	affected, _ := result.RowsAffected()
	return queryResponse{LastInsertID: lastID, RowsAffected: affected}
}

func toDriverValues(params []any) []any {
	if len(params) == 0 {
		return nil
	}
	result := make([]any, len(params))
	for i, p := range params {
		result[i] = p
	}
	return result
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

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errorResponse{Error: msg})
	fmt.Fprintf(os.Stderr, "sqlite-server: %s\n", msg)
}
