package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go.naturallyfunny.dev/spotify"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store implements spotify.TokenStore backed by PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
	dsn  string
}

func New(pool *pgxpool.Pool, dsn string) *Store {
	return &Store{pool: pool, dsn: dsn}
}

func (s *Store) GetRefreshToken(ctx context.Context, userID string) (string, error) {
	var token string
	err := s.pool.QueryRow(ctx,
		`SELECT refresh_token FROM spotify_tokens WHERE owner = $1`, userID,
	).Scan(&token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", spotify.ErrNotConnected
		}
		return "", fmt.Errorf("get refresh token: %w", err)
	}
	return token, nil
}

// SaveRefreshToken inserts or replaces the refresh token for userID.
func (s *Store) SaveRefreshToken(ctx context.Context, userID, refreshToken string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO spotify_tokens (owner, refresh_token)
		 VALUES ($1, $2)
		 ON CONFLICT (owner) DO UPDATE SET refresh_token = EXCLUDED.refresh_token`,
		userID, refreshToken,
	)
	if err != nil {
		return fmt.Errorf("save refresh token: %w", err)
	}
	return nil
}

// Migrate runs all pending database migrations.
func (s *Store) Migrate() error {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, s.dsn)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
