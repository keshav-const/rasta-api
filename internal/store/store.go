// Package store is the data-access layer over the Rasta graph in Supabase Postgres.
// It wraps a pgxpool.Pool; every method runs a short query and returns — we never
// hold a connection across other work (the Supabase pooler drops idle connections).
//
// M1 hand-writes queries with pgx to get moving; later milestones introduce sqlc
// (type-safe codegen from queries/*.sql) for the larger read surface.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

// Trust is Rasta's provenance signal — how well-sourced an entity is, as opposed
// to how good the place is (that's Score). This is the product's headline: you can
// see, and filter by, how strongly a fact is corroborated.
type Trust struct {
	Sources      int     `json:"sources"`       // distinct sources backing it
	Platforms    int     `json:"platforms"`     // distinct platforms (cross-platform = stronger)
	LatestSource *string `json:"latest_source"` // YYYY-MM-DD of the newest source, nullable
	Score        float64 `json:"score"`         // 0..1 corroboration score (see computeTrust)
}

// computeTrust turns the raw provenance counts into a 0..1 corroboration score.
// It deliberately rewards INDEPENDENT corroboration over a single loud source:
//   - breadth   (0.55): more distinct sources — saturates at 4
//   - diversity (0.25): more distinct PLATFORMS — a YouTube+Wikipedia+blog claim
//                       is far more trustworthy than three of the same kind
//   - recency   (0.20): newer sources; full within ~1y, decaying to 0 over ~5y more
// The weights live here (not in the DB) so they're one testable place to tune.
func computeTrust(sources, platforms int, latest *time.Time) Trust {
	breadth := math.Min(float64(sources), 4) / 4
	diversity := math.Min(float64(platforms-1), 2) / 2 // 1 platform -> 0, 3+ -> 1
	recency := 0.0
	var latestStr *string
	if latest != nil {
		ageDays := time.Since(*latest).Hours() / 24
		recency = clamp01(1 - (ageDays-365)/1825) // >=1y old starts decaying; ~6y -> 0
		s := latest.Format("2006-01-02")
		latestStr = &s
	}
	score := 0.55*breadth + 0.25*diversity + 0.20*recency
	return Trust{
		Sources:      sources,
		Platforms:    platforms,
		LatestSource: latestStr,
		Score:        math.Round(score*100) / 100,
	}
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

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
	Trust      Trust    `json:"trust"`
}

// PlacesParams are the supported filters + paging for ListPlaces. Zero values
// mean "not set" (empty string / 0), so callers only fill what they need.
type PlacesParams struct {
	City         string // destination slug, e.g. "jaipur"
	Country      string // e.g. "India"
	Category     string // poi_category, e.g. "temple"
	EntityType   string // poi | activity | festival | craft | art_form | ...
	Q            string // name search
	MinSources   int    // only places backed by >= N distinct sources
	MinPlatforms int    // only places corroborated across >= N distinct platforms
	Sort         string // "" (recommendation score) | "trust" (best-corroborated first)
	Limit        int
	Offset       int
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
	if p.MinPlatforms > 0 {
		add("ev.plats >= $%d", p.MinPlatforms) // references the lateral below
	}
	if p.Q != "" {
		add("poi.name ILIKE '%%' || $%d || '%%'", p.Q) // %% is a literal % after fmt
	}

	// Sort: best-corroborated first when asked, else recommendation score.
	orderBy := "poi.recommendation_score desc nulls last, poi.id"
	if p.Sort == "trust" {
		orderBy = "ev.plats desc nulls last, ev.src desc nulls last, " +
			"poi.recommendation_score desc nulls last, poi.id"
	}

	args = append(args, p.Limit, p.Offset)
	// The lateral computes per-entity provenance (distinct sources, distinct
	// platforms, newest source date) from the evidence. idx_evidence_entity makes
	// each one an index lookup; the graph is small, so this is ms-scale. If it ever
	// gets hot, precompute these into columns during scoring (see DECISIONS ADR-007).
	query := fmt.Sprintf(`
		select poi.slug, poi.name, poi.entity_type, poi.poi_category,
		       d.slug, d.country, poi.lat, poi.lng,
		       coalesce(ev.src, 0), coalesce(ev.plats, 0), ev.latest,
		       coalesce(poi.recommendation_score, 0)
		from points_of_interest poi
		join destinations d on d.id = poi.destination_id
		left join lateral (
			select count(distinct ee.source_id) as src,
			       count(distinct s.platform)   as plats,
			       max(coalesce(ee.source_upload_date, s.upload_date)) as latest
			from entity_evidence ee
			join sources s on s.id = ee.source_id
			where ee.entity_id = poi.id
		) ev on true
		where %s
		order by %s
		limit $%d offset $%d`,
		strings.Join(where, " and "), orderBy, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // guaranteed to run when the function returns

	places := make([]Place, 0) // non-nil empty slice -> serializes as [] not null
	for rows.Next() {
		var pl Place
		var plats int
		var latest *time.Time
		if err := rows.Scan(
			&pl.Slug, &pl.Name, &pl.EntityType, &pl.Category,
			&pl.City, &pl.Country, &pl.Lat, &pl.Lng,
			&pl.Sources, &plats, &latest, &pl.Score,
		); err != nil {
			return nil, err
		}
		pl.Trust = computeTrust(pl.Sources, plats, latest)
		places = append(places, pl)
	}
	return places, rows.Err() // surface any error that ended the iteration
}

// --- Place detail + evidence (the "receipts" endpoint) ---------------------

// Evidence is one source's testimony about a place: which source said it, how it
// framed the place (stance), and the structured claims it made. This is Rasta's
// moat — every place is backed by citable evidence, not anonymous star ratings.
type Evidence struct {
	Source     EvidenceSource  `json:"source"`
	Stance     string          `json:"stance"` // recommended | mentioned | warned_against | mixed
	Strength   *float64        `json:"strength"`
	Confidence *float64        `json:"confidence"`
	// Claims is passed through verbatim from the DB (jsonb): each is roughly
	// {field, value, evidence, timestamp_seconds, confidence}. We keep it as raw
	// JSON so the API never silently drops a claim field the extractor added.
	Claims json.RawMessage `json:"claims"`
}

// EvidenceSource is the citation itself: where a claim came from and who made it.
type EvidenceSource struct {
	Platform   string  `json:"platform"` // youtube | wikipedia | blog | podcast
	Title      *string `json:"title"`
	URL        string  `json:"url"`
	Creator    *string `json:"creator"`
	UploadDate *string `json:"upload_date"` // YYYY-MM-DD, nullable
}

// PlaceDetail is one place plus every source that backs it.
type PlaceDetail struct {
	Place
	Evidence []Evidence `json:"evidence"`
}

// GetPlace returns one active place by slug together with all of its evidence,
// or (nil, nil) when no active place has that slug — the handler turns that into
// a 404. Slugs are unique per destination, not globally (see the init schema's
// `unique(destination_id, slug)`), so if two cities happen to share a slug we
// return the highest-scored one. Two short queries, each over one pooled
// connection: the place row, then its evidence rows — we never hold a connection
// across other work.
func (s *Store) GetPlace(ctx context.Context, slug string) (*PlaceDetail, error) {
	var d PlaceDetail
	var id string // POI uuid, needed for the evidence query
	err := s.pool.QueryRow(ctx, `
		select poi.id, poi.slug, poi.name, poi.entity_type, poi.poi_category,
		       dd.slug, dd.country, poi.lat, poi.lng,
		       poi.frequency_count, coalesce(poi.recommendation_score, 0)
		from points_of_interest poi
		join destinations dd on dd.id = poi.destination_id
		where poi.is_active and poi.slug = $1
		order by poi.recommendation_score desc nulls last, poi.id
		limit 1`, slug).Scan(
		&id, &d.Slug, &d.Name, &d.EntityType, &d.Category,
		&d.City, &d.Country, &d.Lat, &d.Lng, &d.Sources, &d.Score,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // not found, not an error
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		select src.platform, src.title, src.url, src.creator_name, ee.source_upload_date,
		       ee.stance, ee.recommendation_strength, ee.extraction_confidence, ee.claims
		from entity_evidence ee
		join sources src on src.id = ee.source_id
		where ee.entity_id = $1
		order by ee.recommendation_strength desc nulls last, ee.created_at desc`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	d.Evidence = make([]Evidence, 0) // [] not null when a place somehow has none
	platforms := map[string]bool{}  // for the trust diversity signal
	var latest *time.Time           // newest source date across the evidence
	for rows.Next() {
		var ev Evidence
		var upload *time.Time // DB `date` scans into time.Time; may be NULL
		if err := rows.Scan(
			&ev.Source.Platform, &ev.Source.Title, &ev.Source.URL, &ev.Source.Creator,
			&upload, &ev.Stance, &ev.Strength, &ev.Confidence, &ev.Claims,
		); err != nil {
			return nil, err
		}
		if ev.Source.Platform != "" {
			platforms[ev.Source.Platform] = true
		}
		if upload != nil {
			if latest == nil || upload.After(*latest) {
				latest = upload
			}
			day := upload.Format("2006-01-02")
			ev.Source.UploadDate = &day
		}
		d.Evidence = append(d.Evidence, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// One evidence row per distinct source (unique(entity_id, source_id)), so the
	// evidence count IS the distinct-source count — same basis as the list endpoint.
	d.Trust = computeTrust(len(d.Evidence), len(platforms), latest)
	return &d, nil
}

// --- Cities (destinations) -------------------------------------------------

// City is one destination — a country/state/city/region the graph anchors
// entities to — with a LIVE count of the active entities anchored to it.
type City struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // country | state | city | region
	Country string   `json:"country"`
	Lat     *float64 `json:"lat"`
	Lng     *float64 `json:"lng"`
	Places  int      `json:"places"` // active entities anchored here (live count)
}

// CitiesParams are the filters + paging for ListCities. Zero = not set.
type CitiesParams struct {
	Country   string // e.g. "India"
	Type      string // country | state | city | region
	Q         string // name search
	MinPlaces int    // only destinations with >= N active entities
	Limit     int
	Offset    int
}

// ListCities returns destinations with a live count of their active entities,
// busiest first. It GROUPs the places table by destination and counts — the
// count lives in the ON clause (not WHERE) so a destination with zero active
// places still comes back with places=0 (a LEFT JOIN that WHERE-filtered the
// right side would instead drop those rows). min_places is an aggregate filter,
// so it goes in HAVING, not WHERE — still fully parameterized.
func (s *Store) ListCities(ctx context.Context, p CitiesParams) ([]City, error) {
	where := []string{"true"}
	var args []any
	add := func(condFmt string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(condFmt, len(args)))
	}
	if p.Country != "" {
		add("d.country = $%d", p.Country)
	}
	if p.Type != "" {
		add("d.type = $%d", p.Type)
	}
	if p.Q != "" {
		add("d.name ILIKE '%%' || $%d || '%%'", p.Q)
	}

	having := ""
	if p.MinPlaces > 0 {
		args = append(args, p.MinPlaces)
		having = fmt.Sprintf("having count(poi.id) >= $%d", len(args))
	}

	args = append(args, p.Limit, p.Offset)
	query := fmt.Sprintf(`
		select d.slug, d.name, d.type, d.country, d.lat, d.lng,
		       count(poi.id) as places
		from destinations d
		left join points_of_interest poi
		       on poi.destination_id = d.id and poi.is_active
		where %s
		group by d.id
		%s
		order by places desc, d.name
		limit $%d offset $%d`,
		strings.Join(where, " and "), having, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cities := make([]City, 0)
	for rows.Next() {
		var c City
		if err := rows.Scan(&c.Slug, &c.Name, &c.Type, &c.Country, &c.Lat, &c.Lng, &c.Places); err != nil {
			return nil, err
		}
		cities = append(cities, c)
	}
	return cities, rows.Err()
}

// --- Entity taxonomy -------------------------------------------------------

// EntityTypeCount is one entity_type and how many active entities have it —
// the taxonomy that powers "browse by category" (poi, festival, sport, ...).
type EntityTypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// ListEntityTypes returns every entity_type present in the active graph with its
// count, most common first — a single-table GROUP BY, no filters.
func (s *Store) ListEntityTypes(ctx context.Context) ([]EntityTypeCount, error) {
	rows, err := s.pool.Query(ctx, `
		select entity_type, count(*) as n
		from points_of_interest
		where is_active
		group by entity_type
		order by n desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]EntityTypeCount, 0)
	for rows.Next() {
		var e EntityTypeCount
		if err := rows.Scan(&e.Type, &e.Count); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
