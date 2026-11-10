package store

import (
	"testing"
	"time"
)

func TestComputeTrust(t *testing.T) {
	// A single source on one platform should score low.
	if got := computeTrust(1, 1, nil).Score; got >= 0.3 {
		t.Errorf("single source: want < 0.3, got %v", got)
	}
	// Plenty of sources across 3 platforms saturates to 1.0.
	if got := computeTrust(9, 3, nil).Score; got != 1.0 {
		t.Errorf("well-corroborated: want 1.0, got %v", got)
	}
	// Breadth saturates at 4 sources, so 5 and 9 score the same.
	if computeTrust(5, 3, nil).Score != computeTrust(9, 3, nil).Score {
		t.Error("breadth should saturate at 4 sources")
	}
	// More platforms (diversity) must raise the score.
	if computeTrust(4, 1, nil).Score >= computeTrust(4, 3, nil).Score {
		t.Error("platform diversity should raise the score")
	}
}

func TestComputeFreshnessEnduringIsNil(t *testing.T) {
	for _, et := range []string{"sport", "festival", "art_form", "craft", "cuisine"} {
		if computeFreshness(et, nil, nil) != nil {
			t.Errorf("%s is enduring: freshness should be nil", et)
		}
	}
}

func TestComputeFreshnessVolatile(t *testing.T) {
	cat := "restaurant"
	recent := time.Now().AddDate(0, 0, -10)
	if f := computeFreshness("poi", &cat, &recent); f == nil || f.Label != "fresh" {
		t.Errorf("recent restaurant: want fresh, got %+v", f)
	}
	old := time.Now().AddDate(-3, 0, 0)
	if f := computeFreshness("poi", &cat, &old); f == nil || f.Label != "stale" {
		t.Errorf("3y-old restaurant: want stale, got %+v", f)
	}
	if f := computeFreshness("poi", &cat, nil); f == nil || f.Label != "unknown" {
		t.Errorf("no date: want unknown, got %+v", f)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	tok := encodeCursor(0.5639, "abc-123")
	s, id, ok := decodeCursor(tok)
	if !ok || id != "abc-123" || s != 0.5639 {
		t.Errorf("round trip failed: score=%v id=%q ok=%v", s, id, ok)
	}
	if _, _, ok := decodeCursor("!!!not-base64!!!"); ok {
		t.Error("garbage cursor should not decode")
	}
}
