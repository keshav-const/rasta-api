// Package ratelimit is a plan-aware request limiter backed by Upstash Redis (REST
// API). It enforces two fixed windows per caller — per-minute (burst) and per-day
// (quota/anti-scrape) — in a single atomic Lua EVAL, i.e. one HTTP round-trip per
// request. It fails OPEN: if Redis errors or is unconfigured, requests are allowed,
// so a Redis hiccup degrades protection rather than taking the API down.
package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Atomically bump both windows and return their new counts. Keys expire so the
// windows roll on their own; limits are compared in Go (so one script fits all plans).
const windowScript = `
local m = redis.call('INCR', KEYS[1])
if m == 1 then redis.call('EXPIRE', KEYS[1], 60) end
local d = redis.call('INCR', KEYS[2])
if d == 1 then redis.call('EXPIRE', KEYS[2], 86400) end
return {m, d}`

type Limiter struct {
	url, token string
	http       *http.Client
	enabled    bool
}

func New(url, token string) *Limiter {
	return &Limiter{
		url: url, token: token,
		http:    &http.Client{Timeout: 2 * time.Second},
		enabled: url != "" && token != "",
	}
}

// Enabled reports whether Redis creds are configured (else the limiter is a no-op).
func (l *Limiter) Enabled() bool { return l.enabled }

// Result describes the decision + the headers to surface to the client.
type Result struct {
	Allowed   bool
	Limit     int // the binding window's limit
	Remaining int
	ResetSec  int // seconds until the binding window resets
}

// Allow bumps the per-minute and per-day counters for id and decides if this request
// is within BOTH budgets. On any error it returns (allowed, err) — callers fail open.
func (l *Limiter) Allow(ctx context.Context, id string, perMin, perDay int) (Result, error) {
	minKey := "khoj:rl:min:" + id
	dayKey := "khoj:rl:day:" + id
	body, _ := json.Marshal([]any{"EVAL", windowScript, "2", minKey, dayKey})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		return Result{Allowed: true}, err
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.http.Do(req)
	if err != nil {
		return Result{Allowed: true}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{Allowed: true}, errors.New("ratelimit: upstash status " + resp.Status)
	}

	var out struct {
		Result []int `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Result) != 2 {
		return Result{Allowed: true}, errors.New("ratelimit: bad upstash response")
	}
	m, d := out.Result[0], out.Result[1]

	// Report whichever window the caller is closest to blowing.
	res := Result{Allowed: m <= perMin && d <= perDay}
	if perMin-m <= perDay-d {
		res.Limit, res.Remaining, res.ResetSec = perMin, max(0, perMin-m), 60
	} else {
		res.Limit, res.Remaining, res.ResetSec = perDay, max(0, perDay-d), 86400
	}
	return res, nil
}
