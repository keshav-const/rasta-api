# Khoj API — architecture & Go guide

A narrative walkthrough of **everything** in this service: what each piece does, the Go
concepts behind it, and the *why this / why not that* for every decision. Read this top to
bottom once and you'll understand the whole product.

- **This doc** = the teaching narrative (how it all fits + Go concepts).
- [`DECISIONS.md`](./DECISIONS.md) = terse, numbered decision records (ADR-001→016).
- [`POSITIONING.md`](./POSITIONING.md) = the product strategy (why it's not generic).

---

## 1. What this service is

Khoj is a **read-only HTTP JSON API** over a verified cultural graph of India (4,900+ places
and traditions, each citing its sources). A request comes in → it's authenticated to a plan →
rate-limited → maybe served from cache → otherwise a handler runs a SQL query → the rows become
JSON. That's the whole job. Everything below is in service of doing that **safely, cheaply, and
fast**, and of making the two things nobody else has — **provenance (trust)** and **freshness** —
first-class.

The expensive, slow work (scraping YouTube, running LLMs, geocoding) lives in a *separate* Python
pipeline (see the `rasta`/`ts` repo). This API only *serves* what that pipeline produced. Keeping
"build the data" and "serve the data" in two codebases is the first and most important
architectural line: they have opposite shapes (batch vs. always-on) and opposite languages that
suit them (Python for ML glue, Go for a concurrent server).

---

## 2. Why Go (and why not the alternatives)

**The job:** a long-lived process handling many small, concurrent, latency-sensitive requests,
each fanning out to Postgres and Redis.

- **Why Go fits:** goroutines make "thousands of concurrent requests" cheap and simple; it
  compiles to a **single static binary** (trivial Docker image, fast cold start on Render); the
  standard library's `net/http` is production-grade on its own; and explicit error handling (`if
  err != nil`) suits an always-on service where you must handle every failure path.
- **Why not Python/FastAPI?** It's what the *pipeline* is written in, and it's great for LLM glue,
  but a serving layer shares a GIL, has a heavier deploy, and is slower per request. We
  deliberately don't mix the batch and serving concerns.
- **Why not Node/TypeScript?** Would add a third language with weaker CPU-bound and concurrency
  ergonomics than Go. (The `web/` frontend *is* TypeScript — right tool there.)
- **Why a separate repo?** A clean, self-contained portfolio artifact, and it keeps serving
  concerns out of the data pipeline.

See DECISIONS "Why Go".

---

## 3. Project layout — `cmd/` and `internal/`

```
cmd/
  api/main.go        # the server: wiring + handlers + middleware
  mintkey/main.go    # a CLI tool to issue API keys
internal/
  config/            # env -> Config struct
  httpx/             # the one JSON envelope + error shape
  store/             # data access (all SQL lives here)
  auth/              # API key -> plan, auth middleware
  ratelimit/         # plan-aware limiter (Redis)
  cache/             # response cache + ETag (Redis)
  kv/                # tiny shared Upstash Redis client
docs/                # swagger spec generated from annotations
```

Two Go conventions do real work here:

- **`cmd/<name>/main.go`** is the Go idiom for "a runnable program." We have two binaries — the
  `api` server and the `mintkey` tool — that *share* the same `internal/` packages. That's why
  `mintkey` can reuse `store` and `auth` without any duplication.
- **`internal/`** is a Go *language feature*, not just a folder name: packages under `internal/`
  can only be imported by code rooted at the same parent. So nothing outside this module can
  import `khoj/internal/store` — the compiler **enforces** that our data layer is private. It
  lets us refactor internals freely without worrying about external callers.

**Dependency direction** (who imports whom) is strictly one-way, which prevents import cycles:
```
cmd/api ─▶ auth ─▶ store ─▶ (pgx)
        ├▶ cache ─▶ kv
        ├▶ ratelimit ─▶ kv
        ├▶ httpx
        └▶ config
```
`store` never imports `auth`; `kv` never imports anything of ours. Lower layers don't know about
higher ones.

---

## 4. The request lifecycle (the heart of it)

Every `/v1` request flows through a **middleware chain** before reaching a handler. In Go, a
middleware is just a function `func(http.Handler) http.Handler` — it wraps the next handler and
can act before/after it. Order matters; ours (from `main.go`) is:

```
request
  │
  ▼  (chi built-ins)
RequestID   → tag each request with an id (for logs)
RealIP      → resolve the true client IP from proxy headers
Logger      → structured access log line
Recoverer   → turn a panic into a 500 instead of crashing the server
Timeout(15s)→ a slow query can't tie up a worker forever
CORS        → allow browsers/other origins to call this public API
  │
  ▼  (/v1 only)
auth        → read API key → resolve PLAN → attach to context (or 401)
ratelimit   → check the plan's budget in Redis → 429 if over
cache       → hot GET? serve from Redis (+ ETag/304) and skip the DB
  │
  ▼
handler     → parse query → store method → SQL → JSON envelope
```

Why this order?
- **auth before ratelimit:** the limit depends on the plan, so we must know the plan first.
- **ratelimit before cache:** even a cache hit should count against your budget (so cache can't
  be used to bypass limits), and a 429 should be as cheap as possible.
- **cache after both:** a cache hit still got authenticated + counted, then skips only the
  expensive part (the DB query).
- **Timeout + Recoverer early:** they must wrap *everything* so any downstream panic or stall is
  contained.

This chain is the "scalable core": auth → plan → limit → cache, all before any handler logic.

---

## 5. `config` — environment in, `Config` out

`config.Load()` reads env vars (via `godotenv` locally; Render sets them directly) into a plain
`Config` struct: `Port`, `DatabaseURL`, `RedisRestURL`, `RedisRestToken`. One function, one
struct, loaded once in `main`. **Why a struct and not reading `os.Getenv` everywhere?** So there's
a single, typed, testable place that documents exactly what the service needs to run, and so a
missing-but-required var (`DATABASE_URL`) fails fast at startup instead of mid-request.

---

## 6. `httpx` — one envelope for the whole API

Every response has the *same shape*:
```jsonc
{ "data": …, "meta": … }                    // success
{ "error": { "code": "...", "message": "" } } // failure
```
`httpx.JSON(w, status, data, meta)` and `httpx.Error(w, status, code, message)` are the only two
ways anything writes a response. **Why:** clients write one parser and one error handler for the
whole API; the machine-readable `code` lets them branch on errors without string-matching messages
(which we're free to reword). `meta` carries paging/counts without polluting `data`.

**Go concept — `any` and struct tags:** `Envelope` fields are `any` (Go's `interface{}`), so any
value serializes; the `json:"data,omitempty"` **struct tags** tell `encoding/json` the JSON key and
to omit empty fields. Go's JSON encoding is reflection-based and built into the stdlib — no
library needed.

---

## 7. `store` — the data layer (the biggest, richest package)

All SQL lives here. `Store` wraps a `*pgxpool.Pool`; every method runs a short query and returns.

### 7.1 Why pgx + pgxpool (not `database/sql`, not an ORM)

- **pgx** is the fastest, most Postgres-native Go driver: it speaks the binary protocol and has
  real support for `jsonb`, arrays, `uuid`, etc. `database/sql` + `lib/pq` would lose those native
  types.
- **pgxpool** is a connection pool. **The hard rule** (from the Supabase session pooler): *never
  hold a DB connection across other work* — the pooler drops idle connections. pgxpool hands a
  connection to a query and reclaims it immediately. Every `store` method is "one short query, then
  return," which respects this by construction.
- **Why not an ORM (GORM/ent)?** An ORM hides the SQL. This is a *learning* build and the queries
  are interesting (lateral joins, trigram search, keyset pagination) — we want them visible and
  under our control. We hand-write SQL in M1; `sqlc` (type-safe codegen from `.sql` files) is the
  planned upgrade once the query surface is large. See ADR-002.

### 7.2 Dynamic but always-parameterized SQL

`ListPlaces` builds its `WHERE` from whichever filters are set, but **every value** goes to pgx as
a positional parameter (`$1, $2, …`), never string-concatenated. A small closure records each:
```go
add := func(condFmt string, val any) {
    args = append(args, val)
    where = append(where, fmt.Sprintf(condFmt, len(args))) // e.g. "d.slug = $3"
}
```
**Why:** concatenating values is the classic SQL-injection hole; parameterization makes injection
*impossible by construction*, and Postgres can cache the plan. Only the *structure* (which
conditions exist) is built with `fmt`, never the data. See ADR-003.

### 7.3 Nullable columns = pointers

`Category *string`, `Lat/Lng *float64`, etc. A `nil` pointer serializes to JSON `null`. **Why:** a
cultural entity genuinely *has no* category and an un-geocoded place *has no* lat/lng — that's
semantically `null`, not `0` (which is a real coordinate) or `""`. Go's zero values can't express
"absent," so pointers do. The cost is nil-checks; the benefit is an honest contract. See ADR-004.

### 7.4 Trust & freshness — computed *on read*

Two signals are attached to every place, computed in Go from data the query already fetches:

- **Trust** (`computeTrust`) = *how well-corroborated*, time-independent: `0.70·breadth` (distinct
  sources, saturating at 4) + `0.30·diversity` (distinct *platforms*). A fact backed by 3
  different platforms is far stronger than 3 of the same. See ADR-007.
- **Freshness** (`computeFreshness`) = *how current*, and **type-gated**: `nil` for enduring
  entities (an art form can't go stale); for physical places it decays with the newest source's
  age — fast for volatile categories (restaurants), slow for semi-stable (monuments). See ADR-008.

**Why compute on read instead of storing columns?** No migration, and the numbers are always in
sync with the evidence. The weights live in one testable Go function, not scattered in SQL. The
SQL supplies the *inputs* (source count, platform count, newest date) via a **lateral join**:
```sql
left join lateral (
  select count(distinct ee.source_id) as src,
         count(distinct s.platform)   as plats,
         max(coalesce(ee.source_upload_date, s.upload_date)) as latest
  from entity_evidence ee join sources s on s.id = ee.source_id
  where ee.entity_id = poi.id
) ev on true
```
A `LATERAL` subquery can reference the outer row (`poi.id`) — it runs once per candidate row, each
an index lookup. The graph is small, so this is milliseconds; if it ever gets hot we precompute
columns during scoring (noted in the ADR). **Why remove recency from *trust*?** Because recency
measured *our scrape cadence*, not trustworthiness — it would punish timeless entities and force a
re-scraping treadmill. Freshness (separate, type-gated) is the right home for time. See ADR-007/008.

### 7.5 Geo + fuzzy search (real Postgres depth)

- `near=lat,lng&radius_km=` → a **haversine** distance computed in SQL (`least/greatest` guard
  `acos`'s domain against floating-point rounding), exposed as `distance_km`, sorted nearest-first.
  No PostGIS needed — plain lat/lng floats + math are exact at this scale. See ADR-009.
- `bbox=minLng,minLat,maxLng,maxLat` → a map-viewport filter (`lng between … and lat between …`).
- `q=` → **pg_trgm** trigram search: `(name ILIKE '%q%' OR name % $q)`, ranked by `similarity()`,
  backed by a GIN index. Typo-tolerant (`kathakli` → Kathakali) and *ranked*, unlike plain ILIKE.

### 7.6 Keyset (cursor) pagination

The default sort is `recommendation_score desc, poi.id desc`. The cursor is an opaque base64 of the
last row's `(score, id)`; the next page is:
```sql
where (coalesce(poi.recommendation_score,0), poi.id) < ($score, $id::uuid)
```
**Why keyset over OFFSET?** OFFSET re-scans and skips N rows (slow at depth) and can skip/duplicate
rows when data changes between pages. Keyset *seeks* straight to the cursor — stable and fast at any
depth. It needs a unique, stable ordering, so we offer it only for the default sort (the
trust/freshness/distance/similarity sorts keep OFFSET). The tuple compare needs both keys
descending, which is why the tiebreaker is `id DESC`. See ADR-011.

### 7.7 `keys.go` — API keys

`GetAPIKeyByHash`, `InsertAPIKey`, `TouchKeyUsage`. We store only the **sha256 hash** of a key — a
DB leak can't be used to call the API. A `(nil, nil)` return means "no such active key" → the
handler makes it a 401. (Why not an error? "Not found" isn't a server failure.)

---

## 8. `auth` — API key → plan

The product's tiers live here as a **code table** (`Plans`), not in the DB — one obvious,
reviewable place for limits; the DB only records which plan a key is on.

```go
type Plan struct { Name string; RequestsPerMin, RequestsPerDay, MaxPageSize int; FullEvidence bool }
```

`Middleware(db)` is the gate:
1. Pull the key from `X-API-Key` or `Authorization: Bearer`.
2. No key → **`anonymous`** plan (a real, tight tier — so the public demo works without a key).
3. A key → sha256 → `GetAPIKeyByHash`. Found → its plan; not found → **401**. (Present-but-wrong is
   an error; absent is just anonymous.)
4. Attach the plan to the request **context** and echo `X-Plan`.

**Go concepts here:**
- **Middleware as a closure:** `Middleware(db)` returns `func(http.Handler) http.Handler` — it
  *captures* `db` so the handler can use it. This is how you inject dependencies into middleware.
- **`context.Context`:** Go's standard way to carry request-scoped values (and cancellation) down
  the call chain. We stash the plan with `context.WithValue` and read it later with
  `auth.PlanFromContext(ctx)`. Context values use unexported key types (`type ctxKey int`) so no
  other package can collide with our keys.
- **Fire-and-forget goroutine:** `go db.TouchKeyUsage(...)` updates `last_used_at` without blocking
  the response — a usage write should never slow or fail a request.

**Why opaque keys + hash lookup instead of JWTs?** Revocation is instant (delete/revoke the row);
a JWT would need a separate blocklist to revoke. See ADR-013.

---

## 9. `kv`, `ratelimit`, `cache` — the Redis layer

### 9.1 `kv` — the shared Upstash client
One tiny client both the limiter and cache use, so the HTTP-to-Redis plumbing isn't duplicated.
Upstash exposes Redis over a **REST API**: each command is an HTTPS POST of a JSON array
(`["GET","k"]`), reply `{"result": …}`. **Why REST, not a persistent `redis://` connection?** It's
stateless and firewall-friendly from any host; one HTTP round-trip per op. When no creds are
configured the client is **disabled** and callers no-op — so local dev works without Redis.

### 9.2 `ratelimit` — plan-aware limiter
Two fixed windows per caller — **per-minute** (burst) and **per-day** (quota / anti-scrape) — bumped
in **one atomic Lua `EVAL`** (so it's one round-trip and race-free):
```lua
local m = redis.call('INCR', KEYS[1]); if m==1 then redis.call('EXPIRE',KEYS[1],60) end
local d = redis.call('INCR', KEYS[2]); if d==1 then redis.call('EXPIRE',KEYS[2],86400) end
return {m, d}
```
Limits are compared in Go (one script fits all plans). Over budget → **429** + `RateLimit-*` +
`Retry-After`. Keyed by API-key id, or **client IP for anonymous**.

**Fail-open:** any Redis error → *allow* the request. **Why?** Availability over strictness — a
Redis hiccup should degrade protection, not take the whole API down. **Anti-scraping** is three
layers: plan-sized limits + a tight anonymous daily budget (200/day) + a plan-aware max page size
(anonymous capped at 25 rows) — together making a full-graph scrape without a key infeasible. See
ADR-014.

### 9.3 `cache` — response cache + ETag
A middleware that, for GETs, builds a Redis key from `plan + path + query`, and:
- **Hit:** serve the stored body, set a strong `ETag`, and if the client sent a matching
  `If-None-Match` → **304 Not Modified** (no body). `X-Cache: HIT`.
- **Miss:** run the handler while **buffering** its response (a custom `http.ResponseWriter` that
  captures status + body), cache a clean 200 for 60s, set the ETag, serve it. `X-Cache: MISS`.

**Go concept — wrapping `http.ResponseWriter`:** to cache a handler's output we embed the real
writer in a `bufWriter` struct and override `Write`/`WriteHeader` to capture instead of send. This
"decorator" pattern is how you observe/modify responses in Go middleware.

**Why 60s TTL + key-per-plan?** The graph only changes on a batch run, so 60s is safe and keeps new
data visible within a minute; per-plan keying stops tiers serving each other's page-capped bodies.
Fail-open again (no Redis → just run the handler). See ADR-015.

---

## 10. The handlers (`cmd/api/main.go`)

Handlers are **methods on an `api` struct** (`a.listPlaces`, …) that holds the shared deps
(`db`, `pool`). **Why methods, not the inline closures we started with?** So `swaggo` can attach
`// @...` doc annotations to them (it can't annotate anonymous funcs), and so dependencies live in
one place. Each handler is tiny: parse query params (with forgiving helpers like `atoiOr` so junk
input never errors), clamp `limit` to the plan's `MaxPageSize`, call one `store` method, write the
envelope. All the interesting logic is in `store`; handlers are glue.

Endpoints: `/healthz` (+ DB ping), `/v1/stats`, `/v1/places`, `/v1/places/{slug}` (+ evidence —
the "receipts"), `/v1/cities`, `/v1/entities` (taxonomy, or a type's entities). Routing is
**chi**: a thin layer over stdlib `net/http` giving URL params (`chi.URLParam`) and the composable
middleware chain, without a framework's conventions. See ADR (chi/pgx).

---

## 11. Docs, container, CI

- **OpenAPI/Swagger:** `swag` reads the `// @Summary/@Param/@Router` annotations and generates
  `docs/` (committed, so the deploy needs no `swag` CLI); Swagger UI is served from our own binary
  at `/swagger/`. Free, self-hosted, no SaaS. See ADR-010.
- **Dockerfile:** multi-stage — build a static CGO-free binary (pgx is pure Go) on
  `golang:alpine`, run it on `distroless/static:nonroot` (~tiny, no shell, ships CA certs). Binds
  `:$PORT` so Render injects the port. See ADR-012.
- **CI:** GitHub Actions runs `build` → `vet` → `test` on push/PR. Tests are **unit tests of the
  pure logic** (trust, freshness, cursor, hashing, etc.) so CI needs no database. See ADR-016.

---

## 12. How to add an endpoint (putting it together)

1. Add a `store` method: write the SQL (parameterized!), scan rows into a typed struct, return it.
2. Add a handler method on `api`: parse params, call the store method, `httpx.JSON(...)`.
3. Register it in the router (`r.Get("/v1/...", a.yourHandler)`).
4. Add `// @...` annotations + run `swag init` to update the docs.
5. Add a unit test for any new pure logic.

The middleware (auth, rate limit, cache) applies automatically because it's on the `/v1` group —
you never think about it in a handler. That's the payoff of the layered design.

---

## 13. The one-paragraph summary

A request is authenticated to a **plan**, **rate-limited** against that plan in Redis, possibly
served from a **Redis cache** (with ETag/304), and otherwise handed to a tiny **handler** that
calls one **parameterized SQL** query through a **pgx pool**, computes **trust** and **freshness**
in Go, and writes a consistent **JSON envelope**. Keys are hashed, SQL is injection-proof by
construction, nullable data is honest (`null`, not `0`), pagination is keyset, search is
trigram-fuzzy, and the whole thing is a static binary in a tiny container with green CI. Every one
of those choices has a recorded reason in `DECISIONS.md`.
