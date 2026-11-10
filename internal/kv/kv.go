// Package kv is a tiny Upstash Redis REST client shared by the rate limiter and the
// response cache. Each call is one HTTPS request to the Upstash endpoint; commands
// are sent as a JSON array (["GET","k"]) and the reply is {"result": ...}. When no
// creds are configured the client is disabled and callers treat it as a no-op.
package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type Client struct {
	url, token string
	http       *http.Client
	enabled    bool
}

func New(url, token string) *Client {
	return &Client{
		url: url, token: token,
		http:    &http.Client{Timeout: 2 * time.Second},
		enabled: url != "" && token != "",
	}
}

func (c *Client) Enabled() bool { return c.enabled }

// do sends one Redis command and decodes {"result": ...} into out.
func (c *Client) do(ctx context.Context, cmd []any, out any) error {
	body, _ := json.Marshal(cmd)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("kv: upstash status " + resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Eval runs a Lua script atomically and decodes its result (used by the limiter).
func (c *Client) Eval(ctx context.Context, script string, keys, args []string, out any) error {
	cmd := []any{"EVAL", script, len(keys)}
	for _, k := range keys {
		cmd = append(cmd, k)
	}
	for _, a := range args {
		cmd = append(cmd, a)
	}
	return c.do(ctx, cmd, out)
}

// Get returns the string value at key, or "" if absent.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	var out struct {
		Result *string `json:"result"`
	}
	if err := c.do(ctx, []any{"GET", key}, &out); err != nil {
		return "", err
	}
	if out.Result == nil {
		return "", nil
	}
	return *out.Result, nil
}

// SetEX sets key=value with a TTL in seconds.
func (c *Client) SetEX(ctx context.Context, key, value string, ttlSec int) error {
	var out struct {
		Result string `json:"result"`
	}
	return c.do(ctx, []any{"SET", key, value, "EX", ttlSec}, &out)
}
