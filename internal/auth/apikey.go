// Package auth implements API key authentication -- the whole auth
// surface. No JWT: there is no third-party identity provider to
// federate with, so a token format built for that problem buys nothing
// here. Keys are stdlib crypto: sha256 for storage, constant-time compare
// for verification, crypto/rand for generation.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	keyPrefixLen = 8
	keyRandBytes = 24
	// KeyHeaderPrefix is prepended to every generated key so a key is
	// recognizable at a glance (in logs, in a leaked-secret scanner) and
	// distinguishable from other token formats.
	KeyHeaderPrefix = "tsk_"
)

var ErrInvalidKey = errors.New("auth: invalid or revoked API key")

// Generate creates a new API key for tenantID, returning the raw key
// (shown to the caller exactly once -- it is never stored) and its id.
// The stored prefix is used for fast lookup; the hash is what's actually
// verified.
func Generate(ctx context.Context, db *pgxpool.Pool, tenantID, name string) (rawKey string, keyID string, err error) {
	randPart := make([]byte, keyRandBytes)
	if _, err := rand.Read(randPart); err != nil {
		return "", "", fmt.Errorf("generate key material: %w", err)
	}
	secret := hex.EncodeToString(randPart)
	rawKey = KeyHeaderPrefix + secret
	prefix := rawKey[:len(KeyHeaderPrefix)+keyPrefixLen]

	hash := sha256.Sum256([]byte(rawKey))

	const q = `INSERT INTO api_keys (tenant_id, key_hash, prefix, name) VALUES ($1, $2, $3, $4) RETURNING id`
	err = db.QueryRow(ctx, q, tenantID, hash[:], prefix, name).Scan(&keyID)
	if err != nil {
		return "", "", fmt.Errorf("store api key: %w", err)
	}
	return rawKey, keyID, nil
}

// Verify checks a raw API key and returns the tenant it belongs to.
// Lookup is by prefix (indexed, cheap); the actual match is a constant-time
// comparison of the full key's hash, so prefix collisions can't be used to
// probe for valid keys character-by-character.
func Verify(ctx context.Context, db *pgxpool.Pool, rawKey string) (tenantID string, err error) {
	if len(rawKey) < len(KeyHeaderPrefix)+keyPrefixLen {
		return "", ErrInvalidKey
	}
	prefix := rawKey[:len(KeyHeaderPrefix)+keyPrefixLen]

	var tenant string
	var storedHash []byte
	const q = `SELECT tenant_id, key_hash FROM api_keys WHERE prefix = $1 AND revoked_at IS NULL`
	err = db.QueryRow(ctx, q, prefix).Scan(&tenant, &storedHash)
	if err == pgx.ErrNoRows {
		return "", ErrInvalidKey
	}
	if err != nil {
		return "", fmt.Errorf("look up api key: %w", err)
	}

	computedHash := sha256.Sum256([]byte(rawKey))
	if subtle.ConstantTimeCompare(computedHash[:], storedHash) != 1 {
		return "", ErrInvalidKey
	}
	return tenant, nil
}

// Revoke disables a key immediately.
func Revoke(ctx context.Context, db *pgxpool.Pool, keyID string) error {
	tag, err := db.Exec(ctx, "UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL", keyID)
	if err != nil {
		return fmt.Errorf("revoke api key %s: %w", keyID, err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("auth: key not found or already revoked")
	}
	return nil
}
