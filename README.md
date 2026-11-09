# Rasta API

A **provenance-first cultural data API for India.** 4,900+ creator-verified places
*and* living traditions (crafts, festivals, cuisines, folk sports), where **every claim
cites its source** — queryable by humans and by AI agents.

The Go service over Rasta's verified knowledge graph. Read-only in M1; auth, plans,
rate-limiting and billing land in later milestones (see [`POSITIONING.md`](./POSITIONING.md)).

> **Why it's not generic:** the differentiator isn't "a REST API in Go" — it's a unique,
> sourced dataset served with two signals no stock travel API offers: **trust**
> (how well-corroborated a fact is) and **freshness** (how current it is), plus geo +
> fuzzy search. See [`POSITIONING.md`](./POSITIONING.md) and [`DECISIONS.md`](./DECISIONS.md).

---

## Stack

Go 1.27 · [chi](https://github.com/go-chi/chi) router · [pgx](https://github.com/jackc/pgx)/pgxpool →
Supabase Postgres (session pooler) · `pg_trgm` for fuzzy search. Hand-written SQL in M1
(sqlc later). One JSON envelope, one error model.

## Run

```bash
cp .env.example .env     # set DATABASE_URL (Supabase IPv4 session-pooler URL)
go run ./cmd/api         # listens on :$PORT (default 8080)
curl localhost:8080/healthz
open  localhost:8080/swagger/   # interactive API docs (Swagger UI)
```

**Interactive docs:** Swagger UI is served by the binary itself at **`/swagger/`** (no
external service, no account). The OpenAPI spec is generated from the handler
annotations and committed under `docs/`. To regenerate after changing an endpoint:

```bash
go install github.com/swaggo/swag/cmd/swag@latest   # one-time
swag init -g cmd/api/main.go --parseDependency --parseInternal -o docs
```

## Response envelope

```jsonc
{ "data": <payload>, "meta": { ... } }          // success
{ "error": { "code": "not_found", "message": "" } }   // failure
```

---

## Endpoints

### `GET /healthz`
Liveness + DB reachability.

### `GET /v1/stats`
Headline counts: places, cities, sources, claims.

### `GET /v1/places`
Filtered, ranked list of entities. Every item carries **trust** and (where applicable)
**freshness**.

| query param | meaning |
|---|---|
| `city` | destination slug (e.g. `jaipur`) |
| `country` | e.g. `India` |
| `category` | poi_category (e.g. `temple`, `restaurant`) |
| `type` | entity_type (`poi`, `festival`, `sport`, `cuisine`, …) |
| `q` | **fuzzy** name search (trigram + substring, typo-tolerant, ranked) |
| `min_sources` | only entities backed by ≥ N distinct sources |
| `min_platforms` | only entities corroborated across ≥ N distinct platforms |
| `max_source_age_days` | drop entities whose newest source is older (enduring types exempt) |
| `fresh_only=true` | drop entities whose freshness would be "stale" (band-aware) |
| `near=lat,lng` + `radius_km` | **radius search** (haversine); adds `distance_km`, sorts nearest-first |
| `bbox=minLng,minLat,maxLng,maxLat` | **map-viewport** filter |
| `sort` | `trust` \| `freshness` \| (default) recommendation score |
| `limit` (≤100), `offset` | pagination |

**Example — the query no stock travel API offers:**
```
GET /v1/places?near=28.61,77.21&radius_km=2&category=restaurant&fresh_only=true&sort=trust
→ best-corroborated, currently-fresh restaurants within 2 km, nearest first
```

### `GET /v1/places/{slug}`
One entity **with all its evidence** — the "receipts": each source (platform, title,
url, creator, date), its stance, and the raw claims. The product's moat.

### `GET /v1/cities`
Destinations with a live count of the entities anchored to each.
Params: `country`, `type` (`city`|`region`), `q`, `min_places`, paging.

### `GET /v1/entities`
Without `type`: the **taxonomy** (each entity_type + count) for category discovery.
With `?type=<t>`: entities of that type (same filters/paging as `/v1/places`).

---

## The two signals

**Trust** — how well-corroborated (time-independent): `0.70·breadth` (distinct sources,
caps at 4) + `0.30·diversity` (distinct platforms). Exposed as
`trust: {sources, platforms, latest_source, score}`. See DECISIONS ADR-007.

**Freshness** — how current (time-dependent), **only for things that change**: `null`
for enduring types (art forms, festivals, sports…); for physical places it decays with
the newest source's age — fast for volatile categories (restaurants/hotels), slow for
semi-stable (monuments/parks). Exposed as `freshness: {latest_source, age_days, score,
label}`. See DECISIONS ADR-008.

They're **orthogonal**: a place can be high-trust **and** stale.

---

## Docs

- [`POSITIONING.md`](./POSITIONING.md) — the north star: what this is, why it's not
  generic, the roadmap.
- [`DECISIONS.md`](./DECISIONS.md) — the running ADR log (why Go/chi/pgx, the envelope,
  trust, freshness, geo + search, …).

## Status

M1 (public read) is feature-complete: `/healthz`, `/v1/stats`, `/v1/places`,
`/v1/places/{slug}`, `/v1/cities`, `/v1/entities`, **trust**, **freshness**, **geo +
fuzzy search**. Next: cursor pagination, OpenAPI/Swagger UI, Docker + deploy; then M2
(auth, plans, rate-limiting, caching, CI).
