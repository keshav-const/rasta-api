// Package cache is a GET response cache for the API: a middleware that stores each
// successful JSON response in Redis for a short TTL and serves repeats from there,
// so hot queries skip the database entirely. It also sets a strong ETag and honours
// If-None-Match (304 Not Modified) to save bandwidth. Degrades to a no-op when Redis
// is unconfigured or on any Redis error (fail-open).
package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/keshav-const/rasta-api/internal/auth"
	"github.com/keshav-const/rasta-api/internal/kv"
)

type entry struct {
	CT   string `json:"ct"`   // Content-Type
	ETag string `json:"etag"` // strong validator
	Body string `json:"body"`
}

// bufWriter buffers status + body while headers pass straight through to the real
// writer, so we can both cache the body and compute an ETag.
type bufWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (b *bufWriter) WriteHeader(s int) { b.status = s }
func (b *bufWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.buf.Write(p)
}

// Middleware caches GET responses for ttlSec seconds. The key includes the caller's
// plan (page caps differ by plan) so tiers don't serve each other stale pages.
func Middleware(client *kv.Client, ttlSec int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !client.Enabled() || r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			key := "khoj:cache:" + auth.PlanFromContext(r.Context()).Name + ":" +
				r.URL.Path + "?" + r.URL.RawQuery

			// Cache hit?
			if raw, err := client.Get(r.Context(), key); err == nil && raw != "" {
				var e entry
				if json.Unmarshal([]byte(raw), &e) == nil {
					serve(w, r, e, "HIT")
					return
				}
			}

			// Miss: run the handler, buffering the response.
			bw := &bufWriter{ResponseWriter: w}
			next.ServeHTTP(bw, r)
			if bw.status == 0 {
				bw.status = http.StatusOK
			}
			body := bw.buf.Bytes()

			// Only cache clean 200s.
			if bw.status == http.StatusOK && len(body) > 0 {
				e := entry{
					CT:   firstNonEmpty(w.Header().Get("Content-Type"), "application/json"),
					ETag: etagOf(body),
					Body: string(body),
				}
				if b, err := json.Marshal(e); err == nil {
					_ = client.SetEX(context.Background(), key, string(b), ttlSec)
				}
				serve(w, r, e, "MISS")
				return
			}
			// Non-200: pass through verbatim.
			w.WriteHeader(bw.status)
			_, _ = w.Write(body)
		})
	}
}

// serve writes a cache entry, honouring If-None-Match with a 304.
func serve(w http.ResponseWriter, r *http.Request, e entry, status string) {
	w.Header().Set("ETag", e.ETag)
	w.Header().Set("X-Cache", status)
	if match := r.Header.Get("If-None-Match"); match != "" && match == e.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", e.CT)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(e.Body))
}

func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
