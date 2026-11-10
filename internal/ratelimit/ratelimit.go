// Package ratelimit is a plan-aware request limiter backed by Upstash Redis. It
// enforces two fixed windows per caller — per-minute (burst) and per-day (quota /
// anti-scrape) — in one atomic Lua EVAL (one round-trip). It fails OPEN: if Redis
// errors or is unconfigured, requests are allowed, so a Redis hiccup degrades
// protection rather than taking the API down.
package ratelimit

import (
	"context"
	"errors"

	"github.com/keshav-const/rasta-api/internal/kv"
)

// Atomically bump both windows and return their new counts. Keys expire so the
// windows roll on their own; limits are compared in Go (one script fits all plans).
const windowScript = `
local m = redis.call('INCR', KEYS[1])
if m == 1 then redis.call('EXPIRE', KEYS[1], 60) end
local d = redis.call('INCR', KEYS[2])
if d == 1 then redis.call('EXPIRE', KEYS[2], 86400) end
return {m, d}`

type Limiter struct {
	kv *kv.Client
}

func New(client *kv.Client) *Limiter { return &Limiter{kv: client} }

// Enabled reports whether Redis is configured (else the limiter is a no-op).
func (l *Limiter) Enabled() bool { return l.kv.Enabled() }

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
	var out struct {
		Result []int `json:"result"`
	}
	err := l.kv.Eval(ctx, windowScript,
		[]string{"khoj:rl:min:" + id, "khoj:rl:day:" + id}, nil, &out)
	if err != nil {
		return Result{Allowed: true}, err
	}
	if len(out.Result) != 2 {
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
