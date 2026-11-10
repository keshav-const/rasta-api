// Package auth resolves an API key to a pricing PLAN and attaches it to the request
// context. The plan is the thing everything scalable hangs off: rate limits (M2 Inc
// 2), feature gates, and usage accounting. Keys are matched by sha256 hash — the raw
// key is never stored or logged.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/keshav-const/rasta-api/internal/httpx"
	"github.com/keshav-const/rasta-api/internal/store"
)

// Plan is a pricing tier: the limits + feature gates the API enforces. "anonymous"
// is the keyless public tier, so the live demo keeps working without a key.
type Plan struct {
	Name         string `json:"name"`
	DailyLimit   int    `json:"daily_limit"` // requests/day (enforced in Inc 2)
	RatePerSec   int    `json:"rate_per_sec"`
	FullEvidence bool   `json:"full_evidence"` // example feature gate (Pro+)
}

// Plans is the tier table. Keeping it in code (not the DB) makes limits one obvious,
// reviewable place; the DB only stores which plan a key is on.
var Plans = map[string]Plan{
	"anonymous":  {"anonymous", 500, 5, false},
	"free":       {"free", 5000, 10, false},
	"pro":        {"pro", 100000, 50, true},
	"enterprise": {"enterprise", 2000000, 200, true},
}

func planByName(name string) Plan {
	if p, ok := Plans[name]; ok {
		return p
	}
	return Plans["anonymous"]
}

type ctxKey int

const (
	planCtxKey  ctxKey = iota // the resolved Plan
	keyIDCtxKey               // the api_keys.id, when authenticated
)

// HashKey is the one-way hash used to store and look up a key (sha256, hex).
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func extractKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	return ""
}

// Middleware resolves the caller's plan from their key (X-API-Key or
// Authorization: Bearer), attaches it to the context, and echoes it as X-Plan. No
// key -> anonymous (not an error). A present-but-invalid/revoked key -> 401.
func Middleware(db *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plan := Plans["anonymous"]
			if raw := extractKey(r); raw != "" {
				k, err := db.GetAPIKeyByHash(r.Context(), HashKey(raw))
				if err != nil {
					httpx.Error(w, http.StatusInternalServerError, "internal", "auth lookup failed")
					return
				}
				if k == nil {
					httpx.Error(w, http.StatusUnauthorized, "invalid_key", "invalid or revoked API key")
					return
				}
				plan = planByName(k.Plan)
				go db.TouchKeyUsage(context.Background(), k.ID) // fire-and-forget
				r = r.WithContext(context.WithValue(r.Context(), keyIDCtxKey, k.ID))
			}
			r = r.WithContext(context.WithValue(r.Context(), planCtxKey, plan))
			w.Header().Set("X-Plan", plan.Name)
			next.ServeHTTP(w, r)
		})
	}
}

// PlanFromContext returns the resolved plan (anonymous if unset).
func PlanFromContext(ctx context.Context) Plan {
	if p, ok := ctx.Value(planCtxKey).(Plan); ok {
		return p
	}
	return Plans["anonymous"]
}

// KeyIDFromContext returns the authenticated key id, or "" for anonymous callers.
func KeyIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(keyIDCtxKey).(string); ok {
		return id
	}
	return ""
}
