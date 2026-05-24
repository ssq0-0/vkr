package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
	"github.com/streaming-system/gateway/internal/auth"
)

type Store struct {
	db *sql.DB
}

type ProcessingRun struct {
	ID               int64     `json:"id"`
	UserID           int64     `json:"user_id"`
	MemoryMode       string    `json:"memory_mode"`
	FilterName       string    `json:"filter_name"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	FramesCount      int       `json:"frames_count"`
	Success          bool      `json:"success"`
	ProcessingTimeMs float64   `json:"processing_time_ms"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}

func (s *Store) CreateUser(ctx context.Context, username, password string) (auth.User, error) {
	if username == "" || password == "" {
		return auth.User{}, errors.New("username and password are required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return auth.User{}, err
	}
	var user auth.User
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO users(username, password_hash) VALUES ($1, $2)
		 RETURNING id, username`,
		username, hash,
	).Scan(&user.ID, &user.Username)
	return user, err
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (auth.User, error) {
	var user auth.User
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash FROM users WHERE username = $1`,
		username,
	).Scan(&user.ID, &user.Username, &hash)
	if err != nil {
		return auth.User{}, errors.New("invalid username or password")
	}
	if !auth.CheckPassword(hash, password) {
		return auth.User{}, errors.New("invalid username or password")
	}
	return user, nil
}

func (s *Store) SaveSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_sessions(user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	return err
}

func (s *Store) SaveProcessingRun(ctx context.Context, run ProcessingRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO processing_runs(
			user_id, memory_mode, filter_name, width, height, frames_count,
			success, processing_time_ms, error_message
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		run.UserID, normalizeMemoryMode(run.MemoryMode), run.FilterName, run.Width, run.Height,
		run.FramesCount, run.Success, run.ProcessingTimeMs, run.ErrorMessage,
	)
	return err
}

func (s *Store) RecentRuns(ctx context.Context, userID int64, limit int) ([]ProcessingRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, memory_mode, filter_name, width, height, frames_count,
		        success, processing_time_ms, COALESCE(error_message, ''), created_at
		   FROM processing_runs
		  WHERE user_id = $1
		  ORDER BY created_at DESC
		  LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProcessingRun
	for rows.Next() {
		var run ProcessingRun
		if err := rows.Scan(
			&run.ID, &run.UserID, &run.MemoryMode, &run.FilterName,
			&run.Width, &run.Height, &run.FramesCount, &run.Success,
			&run.ProcessingTimeMs, &run.ErrorMessage, &run.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func normalizeMemoryMode(mode string) string {
	switch mode {
	case "heap", "no_arena":
		return "no_arena"
	default:
		return "arena"
	}
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
	id BIGSERIAL PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
	id BIGSERIAL PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS processing_runs (
	id BIGSERIAL PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	memory_mode TEXT NOT NULL CHECK (memory_mode IN ('arena', 'no_arena')),
	filter_name TEXT NOT NULL,
	width INTEGER NOT NULL,
	height INTEGER NOT NULL,
	frames_count INTEGER NOT NULL DEFAULT 1,
	success BOOLEAN NOT NULL,
	processing_time_ms DOUBLE PRECISION NOT NULL,
	error_message TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS experiment_results (
	id BIGSERIAL PRIMARY KEY,
	user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
	memory_mode TEXT NOT NULL CHECK (memory_mode IN ('arena', 'no_arena')),
	filter_name TEXT NOT NULL,
	requests_count INTEGER NOT NULL,
	concurrency INTEGER NOT NULL,
	rps DOUBLE PRECISION NOT NULL,
	avg_latency_ms DOUBLE PRECISION NOT NULL,
	p50_latency_ms DOUBLE PRECISION NOT NULL,
	p95_latency_ms DOUBLE PRECISION NOT NULL,
	p99_latency_ms DOUBLE PRECISION NOT NULL,
	success_rate DOUBLE PRECISION NOT NULL,
	notes TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
