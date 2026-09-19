package common

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxConnectRetries = 10
	maxRetryBackoff   = 32 * time.Second
)

func GetDBConnectionString() string {
	var missingEnvVars []string

	checkEnvVar := func(envVar, envVarName string) {
		if envVar == "" {
			missingEnvVars = append(missingEnvVars, envVarName)
		}
	}

	dbUser := os.Getenv("POSTGRES_USER")
	checkEnvVar(dbUser, "POSTGRES_USER")

	dbPassword := os.Getenv("POSTGRES_PASSWORD")
	checkEnvVar(dbPassword, "POSTGRES_PASSWORD")

	dbName := os.Getenv("POSTGRES_DB")
	checkEnvVar(dbName, "POSTGRES_DB")

	dbHost := os.Getenv("POSTGRES_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	dbPort := os.Getenv("POSTGRES_PORT")
	if dbPort == "" {
		dbPort = "5432"
	}

	if len(missingEnvVars) > 0 {
		slog.Error("missing required environment variables", "vars", strings.Join(missingEnvVars, ", "))
		os.Exit(1)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", dbUser, dbPassword, dbHost, dbPort, dbName)
}

// GetNATSURL returns the NATS connection URL, defaulting to a local
// dev-mode server.
func GetNATSURL() string {
	if url := os.Getenv("NATS_URL"); url != "" {
		return url
	}
	return "nats://localhost:4222"
}

// GetDurationSeconds reads an integer-seconds env var, returning fallback
// if unset or invalid.
func GetDurationSeconds(envVar string, fallback time.Duration) time.Duration {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		slog.Warn("invalid duration env var, using default", "var", envVar, "value", raw, "default", fallback)
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// ConnectToDatabase creates a connection pool and waits for the database to
// become reachable. pgxpool.New only parses the DSN and opens no connection,
// so readiness is verified with Ping, retried with exponential backoff.
func ConnectToDatabase(ctx context.Context, dbConnectString string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dbConnectString)
	if err != nil {
		return nil, fmt.Errorf("parse database connection string: %w", err)
	}

	backoff := time.Second
	var pingErr error
	for attempt := 0; attempt < maxConnectRetries; attempt++ {
		pingErr = pool.Ping(ctx)
		if pingErr == nil {
			slog.Info("connected to database")
			return pool, nil
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		}

		if backoff < maxRetryBackoff {
			backoff *= 2
			if backoff > maxRetryBackoff {
				backoff = maxRetryBackoff
			}
		}
	}

	pool.Close()
	return nil, fmt.Errorf("database unreachable after %d attempts: %w", maxConnectRetries, pingErr)
}
