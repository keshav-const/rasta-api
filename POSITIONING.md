# Rasta API — positioning & north star

The one doc to re-read before any build decision. It answers: *what is this, why
is it not generic, and what order do we build it in?* `DECISIONS.md` records the
small "why" calls; this records the big one.

---

## The one-liner (use this on the resume / README)

> **Rasta — a provenance-first cultural data API for India.** 8,100+ creator-verified
> places *and* living traditions (crafts, festivals, cuisines, folk sports, dance & folk
> theatre), where **every claim cites its source** — queryable by humans and by AI agents.

Not "a REST API in Go." The Go API is the *vehicle*; the **verified, sourced dataset
nobody else has** is the product.

---

## The original idea

Take the cultural graph the Python pipeline builds — real Indian places and
traditions, each carrying **sourced claims ("receipts")** mined from YouTube creators,
Wikipedia, blogs and podcasts — and serve it as a **scalable, monetized Go REST API**
that developers pay for. Positioning: *"real places, every result cites its sources —
no SEO slop, no fake reviews."* Business layer: self-serve signup → API keys → usage
dashboard → Razorpay paywall (Free / Pro / Enterprise), with the `web/` app
dogfooding the live API.

---

## The honest framing: generic vs. moat

**Table stakes (does NOT differentiate — just has to be clean):** Go, chi, pgx, API-key
auth, rate limiting, Razorpay, Docker, Render. A recruiter has seen 200 of these.

**The moat (where all the uniqueness budget goes):**
1. **A unique, real dataset with provenance** — a verified cultural graph of India
   where every claim is tied to a real source. Rare, and topical (trust /
   anti-AI-slop / verifiability is a 2026 theme).
2. **The pipeline war-stories behind it** — 50× bulk-scoring optimization, concurrent
   multi-provider geocoding, IP-block-aware transcript recovery, LLM extraction at
   scale. These are senior-engineer stories most portfolios lack.

**Rule:** make the plumbing *boringly excellent*; spend novelty on the three features
below, which only *this* dataset makes possible.

---

## The uniqueness trio (build one by one, in this order)

### A — Provenance as a first-class feature  *(the HEADLINE — ✅ SHIPPED)*
Make "trust" a product, not a side effect. Expose a **corroboration / trust score**
per entity: how many *independent* sources back it, across how many platforms, how recent.
- `GET /v1/places/{slug}` returns inline evidence ("receipts"). ✅
- Every place now carries a `trust` object `{sources, platforms, latest_source, score}`
  (score = 0.70·breadth + 0.30·platform-diversity). ✅ see DECISIONS ADR-007
- Recency intentionally excluded — it measured *our* scrape cadence, not entity
  trustworthiness (would punish timeless cultural entities). `latest_source` is info-only.
- List endpoints gained `min_platforms=` (cross-platform corroboration) and `sort=trust`. ✅
- **Pitch:** *the only travel API where you can filter by how well-sourced a fact is.*
- **Freshness (companion axis) ✅ SHIPPED** — a separate, type-gated `freshness` object
  `{latest_source, age_days, score, label}`: `null` for enduring entities, fast-decay for
  volatile places (restaurants/hotels), slow for semi-stable (monuments). Filters
  `max_source_age_days=` / `fresh_only=true` (band-aware) + `sort=freshness`. See ADR-008.
  Pitch sharpens to: *filter by how well-sourced AND how current each result is.*
- **Still open:** stance/agreement signal (do sources agree?); source-type half-life;
  precomputed trust/freshness columns if the on-read lateral gets hot.

### B — Geospatial + hybrid search  *(✅ SHIPPED — real DB depth, not `ILIKE`)*
- `near=lat,lng` (+ `radius_km`) radius search with `distance_km` + nearest-first, and
  `bbox=` map-viewport filter (haversine in SQL, no PostGIS needed). ✅
- **Trigram** fuzzy + ranked search (`pg_trgm` + GIN index) replacing `ILIKE`. ✅ ADR-009
- **Demo:** `near=28.61,77.21&radius_km=2&category=restaurant&fresh_only=true&sort=trust`
  — best-corroborated, fresh restaurants within 2 km, nearest first. A query no stock
  travel API offers.
- **Follow-up:** geocoding-accuracy pass (a few POIs are mis-geocoded; validate coords
  against the destination's region).

### C — AI-native access: MCP server + natural-language query  *(the 2026 wow)*
- An **MCP server** exposing the graph as a tool for AI agents.
- NL endpoint: *"vegetarian thali in Jaipur recommended by ≥2 creators"* → graph query.
- Reuses the pipeline's LLM muscle on the serving side. **Almost nobody has an MCP
  server on their resume yet** — this is the biggest differentiator.

> Mandatory: **A**. Then **B**, then **C**. All three are planned; ship them one by one.

---

## The credibility layer (fundamentals — their *absence* reads as junior)

- **`DECISIONS.md` (ADR log)** — already started; keep it rich. Rare in junior
  portfolios; signals real engineering judgment. A quiet superweapon.
- **Tests + CI** (green badge), **structured logging**, clean error model (done).
- **A live URL with *real* data** (not toy seed) + **great docs + interactive playground**.
- **Concrete numbers** in the README: *"p99 < X ms at Y req/s, cache hit rate Z%"*
  from an actual load test. Numbers beat adjectives.
- **A short writeup** of 2–3 hard problems (50× scoring fix, IP-block recovery, the
  scoring algorithm) — pre-written interview talking points.

---

## Status — what's done (as of 2026-11-16)

- **Data / schema (M0)** ✅ — **8,136 active entities** across **870 destinations**, built
  from **1,454 sources** on 4 platforms (Wikipedia 751, YouTube 409, blogs 267, podcasts 27);
  cultural types incl. first-class `sport`; `country` field; the **evidence model** (entity ↔
  sources ↔ claims). ~1,800 entities are non-place traditions (crafts, festivals, cuisines,
  dishes, dance/performing arts, art forms, sports); physical places are **~93% geocoded**.
- **API core (M1)** ✅ — `/healthz`, `/v1/stats`, `/v1/places` (filters + **keyset
  pagination**), **`/v1/places/{slug}` + inline evidence** (receipts), `/v1/cities`,
  `/v1/entities?type=`, trust + freshness, **geo (`near`/`bbox`) + fuzzy (`pg_trgm`) search**,
  one JSON envelope + error model, **swaggo OpenAPI + Swagger UI**, Dockerfile, **live on Render**.
- **Scalable layer (M2)** ✅ — API-key auth → plan → **plan-aware rate limiting** (Redis
  sliding window) + anti-scraping, **caching (ETag + Redis, 304)**, usage counters, logging,
  **tests + GitHub Actions CI**.
- **Developer portal (M3)** ✅ — Supabase-Auth signup/login; dashboard to **create/revoke
  keys** + **per-key usage**; API reference rendered from the OpenAPI spec (Scalar); live
  landing page + interactive explorer that dogfoods the API. (Next.js `web/`, on Vercel.)
- **Razorpay paywall (M4)** ✅ — Free/Pro/Enterprise; subscription checkout + **webhook →
  plan flip** (Go reads the plan fresh per request, so zero API change). Functionality
  complete; live keys are added to the portal env to go live.

## Roadmap — remaining

- **M5:** consumer site modernization (dogfood the live API; maps + multi-source POI pages).
- **M6:** polish — load-test numbers, writeups, custom domains, SDK/snippets, final QA.
- **Feature C (optional showcase):** MCP server + natural-language query (AI-native).

---

## Reused assets / hard constraints (don't relearn these)
- Supabase Postgres (the graph) via the **IPv4 session pooler**; **never hold a
  connection across other work** (pgxpool handles it; keep handlers short).
- Upstash Redis creds live in `qloo-hackathon/.env` (plan cache / rate-limit / usage).
- `qloo-hackathon` (TasteTrail) is **frozen** — do not touch; tag `hackathon-submission`.
- Commits: **no Claude attribution** (user rule). Push via the `keshav-const` gh account.
