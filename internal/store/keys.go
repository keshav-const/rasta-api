package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// APIKey is a stored key as the auth layer needs it. We never store or return the
// raw key — only its hash is in the DB; this is what a verified lookup resolves to.
type APIKey struct {
	ID         string
	Name       string
	Plan       string
	Prefix     string
	OwnerEmail *string
}

// GetAPIKeyByHash returns the active (non-revoked) key with this sha256 hash, or
// (nil, nil) if there's no match — the caller turns that into a 401.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	var k APIKey
	err := s.pool.QueryRow(ctx, `
		select id::text, coalesce(name,''), coalesce(plan,'free'),
		       coalesce(key_prefix,''), owner_email
		from api_keys
		where key_hash = $1 and revoked_at is null`, hash).Scan(
		&k.ID, &k.Name, &k.Plan, &k.Prefix, &k.OwnerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// TouchKeyUsage stamps last_used_at. Fire-and-forget: errors are ignored so a
// usage write never fails a request.
func (s *Store) TouchKeyUsage(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx, `update api_keys set last_used_at = now() where id = $1::uuid`, id)
}

// InsertAPIKey stores a new key (hash only) and returns its id. Used by the
// mintkey tool until the M3 portal issues keys.
func (s *Store) InsertAPIKey(ctx context.Context, name, plan, hash, prefix, ownerEmail string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		insert into api_keys (name, plan, key_hash, key_prefix, owner_email)
		values ($1, $2, $3, $4, nullif($5, ''))
		returning id::text`, name, plan, hash, prefix, ownerEmail).Scan(&id)
	return id, err
}
