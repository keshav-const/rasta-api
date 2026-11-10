package cache

import "testing"

func TestETagOf(t *testing.T) {
	a := etagOf([]byte(`{"data":1}`))
	b := etagOf([]byte(`{"data":1}`))
	c := etagOf([]byte(`{"data":2}`))
	if a != b {
		t.Error("same body must produce same ETag")
	}
	if a == c {
		t.Error("different bodies must produce different ETags")
	}
	if len(a) < 3 || a[0] != '"' || a[len(a)-1] != '"' {
		t.Errorf("ETag must be a quoted string, got %s", a)
	}
}
