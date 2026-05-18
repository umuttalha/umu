package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	StateDir  string
	StateFile string
	StateDB   string

	globalDB   *sql.DB
	globalOnce sync.Once
	globalErr  error
)

func init() {
	dataDir := os.Getenv("UMU_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umu"
	}
	StateDir = dataDir
	StateFile = filepath.Join(dataDir, "state.json")
	StateDB = filepath.Join(dataDir, "state.db")
}

// Status represents the current state of a project VM.
type Status string

const (
	StatusRunning  Status = "running"
	StatusStopped  Status = "stopped"
	StatusCreating Status = "creating"
	StatusError    Status = "error"
	StatusFrozen   Status = "frozen"
)

// Project holds all metadata for a deployed project.
type Project struct {
	Name       string     `json:"name"`
	Generation int64      `json:"generation"`
	Status     Status     `json:"status"`
	Runtime    string     `json:"runtime"`     // "python" or "deno"
	BridgeName string     `json:"bridge_name"` // e.g. br-myproject
	BridgeIP   string     `json:"bridge_ip"`   // e.g. 10.x.0.1
	Services   []*Service `json:"services"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Service represents a single VM running a specific service in the project.
type Service struct {
	Name         string   `json:"name"`
	IP           string   `json:"ip"`
	GuestIP      string   `json:"guest_ip"`
	GuestIPv4    string   `json:"guest_ipv4,omitempty"`
	GlobalIP     string   `json:"global_ip,omitempty"`
	TAPDevice    string   `json:"tap_device"`
	DiskPath     string   `json:"disk_path"`
	UserDataDisk string   `json:"user_data_disk,omitempty"`
	StateDisk    string   `json:"state_disk,omitempty"`
	RootReadOnly bool     `json:"root_read_only"`
	Ephemeral    bool     `json:"ephemeral,omitempty"`
	SocketPath   string   `json:"socket_path"`
	PID          int      `json:"pid"`
	VCPUs        int      `json:"vcpus"`
	MemoryMB     int      `json:"memory_mb"`
	AlwaysOn     bool     `json:"always_on"`
	Expose       bool     `json:"expose"`
	Volumes      []string `json:"volumes"`
	Version      int      `json:"version"`
	MACAddress   string   `json:"mac_address"`
	KernelArgs   string   `json:"kernel_args,omitempty"`
	ServicePort  int    `json:"service_port"`
}

// Store manages persistent project state backed by SQLite.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

var ErrStaleGeneration = errors.New("stale generation: project was modified by another process")

func getDB() (*sql.DB, error) {
	globalOnce.Do(func() {
		var err error
		globalDB, globalErr = openDB(StateDB)
		_ = err
	})
	return globalDB, globalErr
}

func openDB(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate state: %w", err)
	}
	return db, nil
}

// NewStore creates a new state store using the default singleton database.
func NewStore() (*Store, error) {
	db, err := getDB()
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// NewStoreWithPath creates a store backed by a specific database file path.
// Useful for tests that need an isolated state database.
func NewStoreWithPath(dbPath string) (*Store, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS projects (
		name TEXT PRIMARY KEY,
		status TEXT NOT NULL DEFAULT 'creating',
		runtime TEXT NOT NULL DEFAULT 'python',
		generation INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT '',
		bridge_name TEXT NOT NULL DEFAULT '',
		bridge_ip TEXT NOT NULL DEFAULT '',
		services_json TEXT NOT NULL DEFAULT '[]'
	)`)
	if err != nil {
		return fmt.Errorf("create projects table: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status)`)
	if err != nil {
		return fmt.Errorf("create status index: %w", err)
	}

	return migrateFromJSON(db)
}

func migrateFromJSON(db *sql.DB) error {
	if _, err := os.Stat(StateFile); os.IsNotExist(err) {
		return nil
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	data, err := os.ReadFile(StateFile)
	if err != nil {
		return nil
	}

	var projects map[string]*Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil
	}

	for _, p := range projects {
		if err := insertProject(db, p); err != nil {
			continue
		}
	}

	os.Rename(StateFile, StateFile+".migrated")
	return nil
}

// Register atomically adds a new project and returns its index.
func (s *Store) Register(p *Project) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM projects WHERE name=?)", p.Name).Scan(&exists)
	if err != nil {
		return 0, fmt.Errorf("check project exists: %w", err)
	}
	if exists {
		return 0, fmt.Errorf("project %q already exists", p.Name)
	}

	if err := insertProject(s.db, p); err != nil {
		return 0, err
	}

	var rowID int64
	s.db.QueryRow("SELECT rowid FROM projects WHERE name = ?", p.Name).Scan(&rowID)
	return int(rowID - 1), nil
}

// Save persists a project.
func (s *Store) Save(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var currentGen int64
	err := s.db.QueryRow("SELECT generation FROM projects WHERE name=?", p.Name).Scan(&currentGen)
	if err == sql.ErrNoRows {
		p.Generation = 1
		return insertProject(s.db, p)
	}
	if err != nil {
		return fmt.Errorf("read project: %w", err)
	}

	if currentGen > p.Generation {
		return fmt.Errorf("%w: project %q disk generation=%d > local generation=%d",
			ErrStaleGeneration, p.Name, currentGen, p.Generation)
	}

	p.Generation = currentGen + 1
	servicesJSON, err := json.Marshal(p.Services)
	if err != nil {
		return fmt.Errorf("marshal services: %w", err)
	}

	_, err = s.db.Exec(`UPDATE projects SET
		status=?, runtime=?, generation=?, created_at=?, bridge_name=?, bridge_ip=?, services_json=?
		WHERE name=?`,
		string(p.Status), p.Runtime, p.Generation, p.CreatedAt.Format(time.RFC3339),
		p.BridgeName, p.BridgeIP, string(servicesJSON), p.Name)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return nil
}

// Get retrieves a project by name (deep copy).
func (s *Store) Get(name string) (*Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(name)
}

func (s *Store) get(name string) (*Project, bool) {
	row := s.db.QueryRow("SELECT name, status, runtime, generation, created_at, bridge_name, bridge_ip, services_json FROM projects WHERE name=?", name)

	var p Project
	var status, runtime, createdAt, servicesJSON string
	var gen int64

	err := row.Scan(&p.Name, &status, &runtime, &gen, &createdAt, &p.BridgeName, &p.BridgeIP, &servicesJSON)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}

	p.Status = Status(status)
	p.Runtime = runtime
	p.Generation = gen
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	json.Unmarshal([]byte(servicesJSON), &p.Services)

	return &p, true
}

// Delete removes a project from state.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM projects WHERE name=?", name)
	return err
}

// Reload is a no-op with SQLite (always reads latest committed data).
func (s *Store) Reload() {}

// List returns deep copies of all projects.
func (s *Store) List() []*Project {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT name, status, runtime, generation, created_at, bridge_name, bridge_ip, services_json FROM projects")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []*Project
	for rows.Next() {
		var p Project
		var status, runtime, createdAt, servicesJSON string
		var gen int64

		if err := rows.Scan(&p.Name, &status, &runtime, &gen, &createdAt, &p.BridgeName, &p.BridgeIP, &servicesJSON); err != nil {
			continue
		}
		p.Status = Status(status)
		p.Runtime = runtime
		p.Generation = gen
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		json.Unmarshal([]byte(servicesJSON), &p.Services)
		result = append(result, &p)
	}
	return result
}

func insertProject(db *sql.DB, p *Project) error {
	if p.Generation == 0 {
		p.Generation = 1
	}
	servicesJSON, err := json.Marshal(p.Services)
	if err != nil {
		return fmt.Errorf("marshal services: %w", err)
	}

	createdAt := p.CreatedAt.Format(time.RFC3339)
	if p.CreatedAt.IsZero() {
		createdAt = time.Now().Format(time.RFC3339)
	}

	_, err = db.Exec(`INSERT INTO projects (name, status, runtime, generation, created_at, bridge_name, bridge_ip, services_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, string(p.Status), p.Runtime, p.Generation, createdAt,
		p.BridgeName, p.BridgeIP, string(servicesJSON))
	return err
}
