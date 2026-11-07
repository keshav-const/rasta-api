// Command api is the Rasta API HTTP service.
//
// Wiring: load config -> open a pgx pool to Supabase -> build the chi router with
// the middleware chain -> serve with graceful shutdown.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keshav-const/rasta-api/internal/config"
	"github.com/keshav-const/rasta-api/internal/httpx"
	"github.com/keshav-const/rasta-api/internal/store"
)

func main() {
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	// pgxpool manages a pool of short-lived connections and reconnects as needed —
	// exactly what the Supabase session pooler wants (never hold one across a call).
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	db := store.New(pool)

	// The middleware chain every request flows through.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	// Liveness/readiness: also checks the DB is reachable.
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		if err := pool.Ping(req.Context()); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db_unavailable", "database not reachable")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"}, nil)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Get("/stats", func(w http.ResponseWriter, req *http.Request) {
			st, err := db.Stats(req.Context())
			if err != nil {
				log.Printf("stats: %v", err)
				httpx.Error(w, http.StatusInternalServerError, "internal", "could not load stats")
				return
			}
			httpx.JSON(w, http.StatusOK, st, nil)
		})

		// GET /v1/places?city=&country=&category=&type=&q=&min_sources=&limit=&offset=
		r.Get("/places", func(w http.ResponseWriter, req *http.Request) {
			q := req.URL.Query()
			limit := clampInt(atoiOr(q.Get("limit"), 20), 1, 100)
			offset := maxInt(atoiOr(q.Get("offset"), 0), 0)
			params := store.PlacesParams{
				City:       q.Get("city"),
				Country:    q.Get("country"),
				Category:   q.Get("category"),
				EntityType: q.Get("type"),
				Q:          q.Get("q"),
				MinSources: atoiOr(q.Get("min_sources"), 0),
				Limit:      limit,
				Offset:     offset,
			}
			places, err := db.ListPlaces(req.Context(), params)
			if err != nil {
				log.Printf("places: %v", err)
				httpx.Error(w, http.StatusInternalServerError, "internal", "could not load places")
				return
			}
			meta := map[string]any{"count": len(places), "limit": limit, "offset": offset}
			httpx.JSON(w, http.StatusOK, places, meta)
		})

		// GET /v1/places/{slug} — one place with all its sourced evidence.
		// This is the "receipts" endpoint: the moat of the whole product is that
		// every place can show exactly which creators/sources backed it and what
		// they claimed. {slug} is a URL path parameter, pulled with chi.URLParam.
		r.Get("/places/{slug}", func(w http.ResponseWriter, req *http.Request) {
			slug := chi.URLParam(req, "slug")
			place, err := db.GetPlace(req.Context(), slug)
			if err != nil {
				log.Printf("place %q: %v", slug, err)
				httpx.Error(w, http.StatusInternalServerError, "internal", "could not load place")
				return
			}
			if place == nil {
				httpx.Error(w, http.StatusNotFound, "not_found", "no place with that slug")
				return
			}
			meta := map[string]any{"evidence_count": len(place.Evidence)}
			httpx.JSON(w, http.StatusOK, place, meta)
		})
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Serve in the background, then block until a shutdown signal.
	go func() {
		log.Printf("rasta-api listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// atoiOr parses s as an int, falling back to def on empty/invalid input — so a
// missing or junk ?limit= never errors the request, it just uses the default.
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
