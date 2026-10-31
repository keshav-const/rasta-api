// Package store is the data-access layer over the Rasta graph in Supabase Postgres.
// It wraps a pgxpool.Pool; every method runs a short query and returns — we never
// hold a connection across other work (the Supabase pooler drops idle connections).
//
// M1 hand-writes queries with pgx to get moving; later milestones introduce sqlc
// (type-safe codegen from queries/*.sql) for the larger read surface.
package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Stats are the headline graph numbers (for /v1/stats + the landing page).
type Stats struct {
	Places  int `json:"places"`
	Cities  int `json:"cities"`
	Sources int `json:"sources"`
	Claims  int `json:"claims"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		select
			(select count(*) from points_of_interest where is_active),
			(select count(*) from destinations),
			(select count(*) from sources),
			(select coalesce(sum(jsonb_array_length(claims)), 0)
			   from entity_evidence where jsonb_typeof(claims) = 'array')
	`).Scan(&st.Places, &st.Cities, &st.Sources, &st.Claims)
	return st, err
}
