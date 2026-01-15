package database

import (
	"database/sql"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DownloadStatus string

const (
	StatusPending     DownloadStatus = "pending"
	StatusDownloading DownloadStatus = "downloading"
	StatusPaused      DownloadStatus = "paused"
	StatusCompleted   DownloadStatus = "completed"
	StatusCancelled   DownloadStatus = "cancelled"
	StatusFailed      DownloadStatus = "failed"
)

type Download struct {
	ID              int            `json:"id"`
	URL             string         `json:"url"`
	Filename        string         `json:"filename"`
	Status          DownloadStatus `json:"status"`
	Progress        int            `json:"progress"`
	FileSize        int64          `json:"file_size"`
	DownloadedBytes int64          `json:"downloaded_bytes"`
	Speed           int64          `json:"speed"`
	ETA             int            `json:"eta"`
	Error           string         `json:"error"`
	CreatedAt       string         `json:"created_at"`
	CompletedAt     string         `json:"completed_at"`
}

var db *sql.DB

func InitDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS downloads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT NOT NULL,
		filename TEXT,
		status TEXT DEFAULT 'pending',
		progress INTEGER DEFAULT 0,
		file_size INTEGER,
		downloaded_bytes INTEGER DEFAULT 0,
		speed INTEGER,
		eta INTEGER,
		error TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		completed_at TEXT
	)`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		return err
	}

	// Try to add columns if they don't exist (for existing databases)
	columns := []string{
		"ALTER TABLE downloads ADD COLUMN speed INTEGER",
		"ALTER TABLE downloads ADD COLUMN eta INTEGER",
		"ALTER TABLE downloads ADD COLUMN downloaded_bytes INTEGER DEFAULT 0",
	}

	for _, col := range columns {
		_, _ = db.Exec(col) // Ignore errors if column already exists
	}

	log.Println("Database initialized successfully")
	return nil
}

func AddDownload(url string) (int64, error) {
	result, err := db.Exec(
		"INSERT INTO downloads (url, status) VALUES (?, ?)",
		url, StatusPending,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func UpdateDownload(downloadID int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	// Handle completed status
	if status, ok := updates["status"]; ok && status == StatusCompleted {
		updates["completed_at"] = time.Now().Format(time.RFC3339)
		updates["eta"] = 0
		updates["speed"] = 0
	}

	query := "UPDATE downloads SET "
	args := []interface{}{}

	first := true
	for key, value := range updates {
		if !first {
			query += ", "
		}
		query += key + " = ?"
		args = append(args, value)
		first = false
	}

	query += " WHERE id = ?"
	args = append(args, downloadID)

	_, err := db.Exec(query, args...)
	return err
}

func GetDownload(downloadID int64) (*Download, error) {
	download := &Download{}
	err := db.QueryRow(`
		SELECT id, url, COALESCE(filename, ''), status, progress,
		       COALESCE(file_size, 0), COALESCE(downloaded_bytes, 0),
		       COALESCE(speed, 0), COALESCE(eta, 0), COALESCE(error, ''),
		       created_at, COALESCE(completed_at, '')
		FROM downloads WHERE id = ?
	`, downloadID).Scan(
		&download.ID, &download.URL, &download.Filename, &download.Status,
		&download.Progress, &download.FileSize, &download.DownloadedBytes,
		&download.Speed, &download.ETA, &download.Error,
		&download.CreatedAt, &download.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return download, nil
}

func GetDownloads() ([]Download, error) {
	rows, err := db.Query(`
		SELECT id, url, COALESCE(filename, ''), status, progress,
		       COALESCE(file_size, 0), COALESCE(downloaded_bytes, 0),
		       COALESCE(speed, 0), COALESCE(eta, 0), COALESCE(error, ''),
		       created_at, COALESCE(completed_at, '')
		FROM downloads ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	downloads := []Download{}
	for rows.Next() {
		download := Download{}
		err := rows.Scan(
			&download.ID, &download.URL, &download.Filename, &download.Status,
			&download.Progress, &download.FileSize, &download.DownloadedBytes,
			&download.Speed, &download.ETA, &download.Error,
			&download.CreatedAt, &download.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		downloads = append(downloads, download)
	}

	return downloads, nil
}

func DeleteDownload(downloadID int64) error {
	_, err := db.Exec("DELETE FROM downloads WHERE id = ?", downloadID)
	return err
}

func Close() error {
	if db != nil {
		return db.Close()
	}
	return nil
}
