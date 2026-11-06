// Package store is the data-access layer over the Rasta graph in Supabase Postgres.
// It wraps a pgxpool.Pool; every method runs a short query and returns — we never
// hold a connection across other work (the Supabase pooler drops idle connections).
//
// M1 hand-writes queries with pgx to get moving; later milestones introduce sqlc
// (type-safe codegen from queries/*.sql) for the larger read surface.
package store

import (
	"context"
	"fmt"
	"strings"

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

// Place is one graph entity as the API exposes it. Pointer fields (*string,
// *float64) are nullable: a cultural entity has no category, and an un-geocoded
// place has no lat/lng — those come back as JSON null instead of 0/"".
type Place struct {
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	EntityType string   `json:"entity_type"`
	Category   *string  `json:"category"`
	City       string   `json:"city"`
	Country    string   `json:"country"`
	Lat        *float64 `json:"lat"`
	Lng        *float64 `json:"lng"`
	Sources    int      `json:"sources"`
	Score      float64  `json:"score"`
}

// PlacesParams are the supported filters + paging for ListPlaces. Zero values
// mean "not set" (empty string / 0), so callers only fill what they need.
type PlacesParams struct {
	City       string // destination slug, e.g. "jaipur"
	Country    string // e.g. "India"
	Category   string // poi_category, e.g. "temple"
	EntityType string // poi | activity | festival | craft | art_form | ...
	Q          string // name search
	MinSources int    // only places backed by >= N sources
	Limit      int
	Offset     int
}

// ListPlaces returns active entities matching the filters, highest-scored first.
// The SQL is built dynamically but ALWAYS parameterized ($1, $2, …) — values go
// through pgx as parameters, never string-concatenated, so SQL injection is
// impossible by construction.
func (s *Store) ListPlaces(ctx context.Context, p PlacesParams) ([]Place, error) {
	where := []string{"poi.is_active"}
	var args []any
	// add appends one arg and records a condition that references it by position.
	add := func(condFmt string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(condFmt, len(args)))
	}
	if p.City != "" {
		add("d.slug = $%d", p.City)
	}
	if p.Country != "" {
		add("d.country = $%d", p.Country)
	}
	if p.Category != "" {
		add("poi.poi_category = $%d", p.Category)
	}
	if p.EntityType != "" {
		add("poi.entity_type = $%d", p.EntityType)
	}
	if p.MinSources > 0 {
		add("poi.frequency_count >= $%d", p.MinSources)
	}
	if p.Q != "" {
		add("poi.name ILIKE '%%' || $%d || '%%'", p.Q) // %% is a literal % after fmt
	}

	args = append(args, p.Limit, p.Offset)
	query := fmt.Sprintf(`
		select poi.slug, poi.name, poi.entity_type, poi.poi_category,
		       d.slug, d.country, poi.lat, poi.lng,
		       poi.frequency_count, coalesce(poi.recommendation_score, 0)
		from points_of_interest poi
		join destinations d on d.id = poi.destination_id
		where %s
		order by poi.recommendation_score desc nulls last, poi.id
		limit $%d offset $%d`,
		strings.Join(where, " and "), len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // guaranteed to run when the function returns

	places := make([]Place, 0) // non-nil empty slice -> serializes as [] not null
	for rows.Next() {
		var pl Place
		if err := rows.Scan(
			&pl.Slug, &pl.Name, &pl.EntityType, &pl.Category,
			&pl.City, &pl.Country, &pl.Lat, &pl.Lng,
			&pl.Sources, &pl.Score,
		); err != nil {
			return nil, err
		}
		places = append(places, pl)
	}
	return places, rows.Err() // surface any error that ended the iteration
}
