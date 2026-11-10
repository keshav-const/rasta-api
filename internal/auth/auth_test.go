package auth

import (
	"net/http/httptest"
	"testing"
)

func TestHashKey(t *testing.T) {
	if HashKey("khoj_abc") != HashKey("khoj_abc") {
		t.Error("hash must be deterministic")
	}
	if HashKey("a") == HashKey("b") {
		t.Error("different keys must not collide")
	}
	if len(HashKey("x")) != 64 {
		t.Errorf("sha256 hex must be 64 chars, got %d", len(HashKey("x")))
	}
}

func TestExtractKey(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-API-Key", "k1")
	if got := extractKey(r); got != "k1" {
		t.Errorf("X-API-Key: want k1, got %q", got)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer k2")
	if got := extractKey(r); got != "k2" {
		t.Errorf("Bearer: want k2, got %q", got)
	}

	if got := extractKey(httptest.NewRequest("GET", "/", nil)); got != "" {
		t.Errorf("no key: want empty, got %q", got)
	}
}

func TestPlanByName(t *testing.T) {
	if planByName("pro").Name != "pro" {
		t.Error("known plan should resolve")
	}
	if planByName("does-not-exist").Name != "anonymous" {
		t.Error("unknown plan should fall back to anonymous")
	}
}
