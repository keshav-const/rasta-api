# Rasta API — v1 decisions log

A running, plain-English record of **why** this service is built the way it is.
It's a teaching document as much as a reference: each entry states the decision,
the alternatives we weighed, and why we picked what we picked. Newest at the top
of each milestone.

Format per entry: **Decision → Alternatives → Why → Revisit when**.

---

## M1 — public read API (in progress)

### ADR-007 · Trust/corroboration is a computed, first-class signal (Feature A — the headline)
- **Decision:** every place carries a `trust` object — `{sources, platforms, latest_source,
  score}` — where `score` (0..1) weights **breadth** (distinct sources, 0.70) and
  **diversity** (distinct *platforms*, 0.30). List endpoints gain `min_platforms=`
  (cross-platform corroboration) and `sort=trust`.
- **Recency is deliberately NOT in the score** (revised): source-age measures when *we*
  last ingested, not whether the entity is current. A centuries-old art form (Kathakali)
  isn't less trustworthy because our newest source is from 2021 — penalizing that would
  punish enduring/cultural entities for our ingestion gaps and force a perpetual
  re-scraping treadmill. `latest_source` is still exposed as plain info (doesn't move the
  score). A separate, type-aware *freshness* signal for volatile places (restaurants that
  may have closed) can come later.
- **Alternatives:** (a) just expose the raw source count (already there as `sources`);
  (b) precompute a trust column in the pipeline/DB; (c) fold trust into the existing
  `recommendation_score`.
- **Why:** provenance *is* the product's differentiator, so "how well-sourced is this?"
  deserves to be a filterable, sortable, visible number — distinct from `score` ("how
  good is the place"). Raw count alone misses that 3 *different platforms* corroborating
  is far stronger than 3 of the same; the diversity term captures that. We compute it
  **on read** via a lateral over the evidence (not a stored column) so M1 needs no
  migration and the number is always in sync with the evidence; the weights live in
  one Go function (`computeTrust`) so they're testable and tunable in one place.
- **Caveat:** `sort=trust` orders by platforms-then-sources in SQL (can't ORDER BY a
  Go-computed value); the `score` additionally decays by recency, so an old many-source
  entity can sort above a newer one with a higher score. Acceptable for v1.
- **Revisit when:** the lateral gets hot at scale → precompute `trust_*` columns during
  scoring and ORDER BY the stored score (then sort and score agree exactly).


### ADR-006 · `/v1/places/{slug}` returns the place *with* its evidence inline
- **Decision:** one request to `/v1/places/{slug}` returns the place object plus an
  `evidence[]` array — each entry is a source (platform, title, url, creator, date),
  a `stance`, strength/confidence, and the raw `claims`.
- **Alternatives:** (a) a separate `/v1/places/{slug}/evidence` endpoint; (b) return
  only IDs and make the client fetch each source.
- **Why:** the evidence *is* the product ("every place cites its sources"). A place
  without its receipts is the boring half of the data. One round-trip is what a
  consumer page or an LLM actually wants. We can still add a paginated
  `/evidence` sub-resource later if a place ever has hundreds of sources — today the
  max is small, so inlining wins. The heavy, per-plan "full evidence" gating lands
  in M2, not here.
- **Revisit when:** a single place exceeds ~50 evidence rows, or Pro-tier evidence
  gating needs its own cursor.

### ADR-005 · `claims` is passed through as raw JSON, not re-typed in Go
- **Decision:** `Evidence.Claims` is `json.RawMessage` — pgx scans the `jsonb`
  column and we hand it to the client verbatim.
- **Alternatives:** define a `Claim` struct (`field, value, evidence, confidence,
  timestamp_seconds`) and decode into it.
- **Why:** the extractor (an LLM) occasionally adds fields; a rigid struct would
  silently drop them, and the API would quietly lie about what a source said. Raw
  pass-through is lossless and one less place to keep in sync with the pipeline. The
  cost — no compile-time guarantee on claim shape — is acceptable for a read API
  whose claims originate from our own trusted pipeline.
- **Revisit when:** we need to filter/sort *by* a claim field in SQL (then we'd
  promote the hot fields to typed columns or a GIN index), or we expose a
  claims-only endpoint.

### ADR-004 · Nullable columns are pointers (`*string`, `*float64`), serialized as JSON `null`
- **Decision:** `Category`, `Lat`, `Lng`, `Title`, `Creator`, `UploadDate`, etc. are
  pointer fields.
- **Alternatives:** `sql.NullString`/`pgtype.Text` (serialize to `{"String":"","Valid":false}`
  unless customized); or non-pointer zero values (`""`, `0`).
- **Why:** a cultural entity genuinely *has no* category and an un-geocoded place
  *has no* lat/lng — that's semantically `null`, not `0` or `""` (0,0 is a real point
  in the ocean). Pointers give clean `null` in JSON with no wrapper struct. The
  tradeoff is nil-checks in Go code, which is a fair price for an honest contract.
- **Revisit when:** never, probably — this is the idiomatic Go/pgx answer.

### ADR-003 · Dynamic filters are built as **parameterized** SQL, never string-concatenated values
- **Decision:** `ListPlaces` builds its `WHERE` from whichever filters are set, but
  every value goes through pgx as a positional parameter (`$1, $2, …`). An `add()`
  closure appends the arg and records the condition referencing it by position.
- **Alternatives:** a query builder library (squirrel); or formatting values into the
  SQL string.
- **Why:** value-concatenation is the classic SQL-injection hole; parameterization
  makes injection impossible *by construction*, and the DB can also cache the plan.
  A builder lib is more than six optional filters justify. Only the *structure*
  (which conditions exist) is built with `fmt`, never the data.
- **Revisit when:** the filter surface grows past what hand-building stays readable
  for — then adopt sqlc (see ADR-002) or a builder.

### ADR-002 · M1 hand-writes SQL with pgx; sqlc comes in a later milestone
- **Decision:** queries are hand-written Go strings against `pgxpool` for now.
- **Alternatives:** sqlc (type-safe codegen from `.sql` files) now; or a full ORM
  (GORM, ent).
- **Why:** we're learning the stack and the query surface is tiny (stats, places,
  place-detail). Hand-written pgx keeps the whole request path visible with zero
  codegen or ORM magic to reason about — best for understanding *and* for control
  over the exact SQL. sqlc earns its keep once the read surface is large and we want
  compile-time guarantees across many queries; an ORM hides the SQL, which is the
  opposite of what a learning build wants.
- **Revisit when:** the query count grows (M2+) or we want typed query signatures —
  switch to sqlc then.

### ADR-001 · One JSON envelope for every response: `{data, meta}` / `{error:{code,message}}`
- **Decision:** success is `{"data": …, "meta": …}`; failure is
  `{"error": {"code", "message"}}` with a machine-readable `code`.
- **Alternatives:** return bare arrays/objects; or model errors as HTTP status only.
- **Why:** one predictable shape across the whole API means clients write one parser
  and one error handler. `meta` carries paging/counts without polluting `data`. The
  stable `code` lets clients branch on errors without string-matching messages (which
  we're free to reword). This is the contract everything else hangs off.
- **Revisit when:** we add cursor pagination (M1 tail) — `meta` gains a `next_cursor`
  but the envelope shape doesn't change.

---

## M0 — platform choices (decided at kickoff)

### Why Go for this service
- **Decision:** the public API is a standalone Go service (new `rasta-api` repo).
- **Alternatives considered:** keep it in the existing Python pipeline (FastAPI);
  a Node/TypeScript service alongside `web/`.
- **Why:** the pipeline (Python) and the serving layer have opposite shapes — the
  pipeline is batch, LLM-bound, run-when-we-say; the API is long-lived, concurrent,
  latency-sensitive, and will fan out to Redis + Postgres per request. Go's
  goroutines + a real connection pool (`pgxpool`) handle many concurrent short
  requests with a tiny memory footprint, it compiles to one static binary (trivial
  container, fast cold start on Render), and its explicit error handling suits an
  always-on service. Keeping it a *separate repo* also makes it a clean portfolio
  artifact and keeps serving concerns out of the data pipeline. Python/FastAPI would
  work but shares a GIL and a heavier deploy; Node would mean a third language with
  weaker CPU-bound and concurrency ergonomics than Go.
- **Revisit when:** never for v1 — this is a foundational bet we're deliberately making.

### `chi` router · `pgx`/`pgxpool` driver · `godotenv` config
- **Decision:** `chi` for routing/middleware, `pgx` (+ `pgxpool`) straight to Postgres,
  `godotenv` for local env.
- **Alternatives:** stdlib `net/http` mux (pre-1.22 pattern limits); Gin/Echo
  (batteries-included frameworks); `database/sql` + `lib/pq`.
- **Why:** `chi` is a thin layer *over* stdlib `net/http` — idiomatic handlers, a
  composable middleware chain, URL params — without a framework's conventions to
  learn or fight. `pgx` is the fastest, most Postgres-native Go driver (binary
  protocol, real `jsonb`/array/`uuid` support, a pool that matches the Supabase
  session-pooler rule of never holding a connection across other work). Gin/Echo
  would add surface area we don't need; `database/sql` would cost us pgx's
  Postgres-native types.
- **Revisit when:** not expected; these are the stack's load-bearing picks.

### Connection discipline: short queries over a pool, never held across work
- **Decision:** every store method runs a short query and returns; `pgxpool` owns the
  connections.
- **Why:** the Supabase **session pooler** drops idle connections — holding one across
  an LLM/network call (as the pipeline learned the hard way) breaks. Handlers stay
  short and the pool reconnects as needed. Use the IPv4 session-pooler URL, not the
  IPv6 direct endpoint.
- **Revisit when:** never — this is a hard constraint of the hosting.
