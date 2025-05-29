package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "github.com/joho/godotenv/autoload"
	_ "github.com/mattn/go-sqlite3"
)

// Service represents a service that interacts with a database.
type Service interface {
	// Health returns a map of health status information.
	// The keys and values in the map are service-specific.
	Health() map[string]string

	// Close terminates the database connection.
	// It returns an error if the connection cannot be closed.
	Close() error

	// GetUploadedFiles retrieves the list of uploaded files from the database.
	GetUploadedFiles(ctx context.Context) ([]map[string]interface{}, error)

	// Exec executes a query with the given arguments.
	Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error)

	// Reset deletes all records from the files table.
	Reset(ctx context.Context) error

	// CreateFilesTable creates the files table if it doesn't exist.
	CreateFilesTable() error
}

type service struct {
	db *sql.DB
}

var (
	dbInstance *service
	dbMutex    sync.Mutex
)

// ensureConnection ensures that the database connection is active and valid
func ensureConnection(s *service) error {
	if s.db == nil {
		return fmt.Errorf("database connection is nil")
	}

	// Try to ping the database
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := s.db.PingContext(ctx); err != nil {
		log.Printf("Database connection lost, attempting to reconnect...")
		return initializeDB()
	}

	return nil
}

func initializeDB() error {
	// Get database URL from environment or use default
	dburl := os.Getenv("BLUEPRINT_DB_URL")
	if dburl == "" {
		// Create data directory if it doesn't exist
		if err := os.MkdirAll("data", 0755); err != nil {
			return fmt.Errorf("failed to create data directory: %w", err)
		}
		dburl = "data/rclone_uploader.db"
		log.Printf("BLUEPRINT_DB_URL not set, using default: %s", dburl)
	} else {
		log.Printf("Using database URL from environment: %s", dburl)
		// Ensure the directory for the database file exists
		dir := filepath.Dir(dburl)
		if dir != "." && dir != "/" {
			log.Printf("Creating directory for database: %s", dir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Printf("Failed to create database directory %s: %v", dir, err)
				return fmt.Errorf("failed to create database directory %s: %w", dir, err)
			}
		}
	}

	log.Printf("Attempting to open database at: %s", dburl)
	db, err := sql.Open("sqlite3", dburl)
	if err != nil {
		log.Printf("Failed to open database %s: %v", dburl, err)
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(1) // SQLite only supports one writer at a time
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	dbInstance = &service{db: db}

	// Create files table
	if err := dbInstance.CreateFilesTable(); err != nil {
		db.Close()
		return fmt.Errorf("failed to create files table: %w", err)
	}

	return nil
}

func New() Service {
	dbMutex.Lock()
	defer dbMutex.Unlock()

	if dbInstance != nil {
		// Ensure the connection is still valid
		if err := ensureConnection(dbInstance); err != nil {
			log.Printf("Error ensuring database connection: %v", err)
			// Connection is invalid, try to reinitialize
			if err := initializeDB(); err != nil {
				log.Fatal(err)
			}
		}
		return dbInstance
	}

	// Initialize new database connection
	if err := initializeDB(); err != nil {
		log.Fatal(err)
	}

	return dbInstance
}

// Health checks the health of the database connection
func (s *service) Health() map[string]string {
	stats := make(map[string]string)

	// Check connection
	if err := ensureConnection(s); err != nil {
		stats["status"] = "down"
		stats["error"] = fmt.Sprintf("db down: %v", err)
		return stats
	}

	// Database is up, add more statistics
	stats["status"] = "up"
	stats["message"] = "It's healthy"

	// Get database stats
	dbStats := s.db.Stats()
	stats["open_connections"] = strconv.Itoa(dbStats.OpenConnections)
	stats["in_use"] = strconv.Itoa(dbStats.InUse)
	stats["idle"] = strconv.Itoa(dbStats.Idle)
	stats["wait_count"] = strconv.FormatInt(dbStats.WaitCount, 10)
	stats["wait_duration"] = dbStats.WaitDuration.String()
	stats["max_idle_closed"] = strconv.FormatInt(dbStats.MaxIdleClosed, 10)
	stats["max_lifetime_closed"] = strconv.FormatInt(dbStats.MaxLifetimeClosed, 10)

	return stats
}

// Close closes the database connection.
func (s *service) Close() error {
	// Don't actually close the connection since it's a singleton
	return nil
}

func (s *service) CreateFilesTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		uploaded_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err := s.db.Exec(query)
	return err
}

// GetUploadedFiles retrieves all uploaded files from the database
func (s *service) GetUploadedFiles(ctx context.Context) ([]map[string]interface{}, error) {
	if err := ensureConnection(s); err != nil {
		return nil, fmt.Errorf("database connection error: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT name, path, uploaded_at 
		FROM files 
		ORDER BY uploaded_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query files: %w", err)
	}
	defer rows.Close()

	var files []map[string]interface{}
	for rows.Next() {
		var name, path string
		var uploadedAt time.Time
		if err := rows.Scan(&name, &path, &uploadedAt); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		files = append(files, map[string]interface{}{
			"name":        name,
			"path":        path,
			"uploaded_at": uploadedAt,
		})
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return files, nil
}

func (s *service) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if err := ensureConnection(s); err != nil {
		return nil, fmt.Errorf("database connection error: %w", err)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func (s *service) Reset(ctx context.Context) error {
	if err := ensureConnection(s); err != nil {
		return fmt.Errorf("database connection error: %w", err)
	}

	query := `DELETE FROM files`
	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to reset database: %w", err)
	}
	return nil
}
