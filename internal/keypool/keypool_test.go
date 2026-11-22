package keypool

import "testing"

func TestRotationAndInvalidation(t *testing.T) {
	p := New([]string{"a", "b", "c"})
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		_, k, ok := p.Next()
		if !ok {
			t.Fatal("expected a key")
		}
		seen[k]++
	}
	if len(seen) != 3 {
		t.Fatalf("rotation did not cover all keys: %v", seen)
	}
	for k, n := range seen {
		if n != 2 {
			t.Fatalf("key %s used %d times", k, n)
		}
	}

	p.Invalidate(1)
	for i := 0; i < 4; i++ {
		_, k, ok := p.Next()
		if !ok {
			t.Fatal("expected key")
		}
		if k == "b" {
			t.Fatal("invalidated key returned")
		}
	}
	if got := p.Available(); got != 2 {
		t.Fatalf("available = %d", got)
	}

	p.Invalidate(0)
	p.Invalidate(2)
	if _, _, ok := p.Next(); ok {
		t.Fatal("expected no available keys")
	}
}

func TestEmptyPoolMeansNoAuth(t *testing.T) {
	p := New(nil)
	idx, key, ok := p.Next()
	if !ok || idx != -1 || key != "" {
		t.Fatalf("empty pool should mean no auth, got %d %q %v", idx, key, ok)
	}
}

func TestManagerReusesPoolOnSameKeys(t *testing.T) {
	m := NewManager()
	p1 := m.Get("glm", []string{"a", "b"})
	p1.Invalidate(0)
	p2 := m.Get("glm", []string{"a", "b"})
	if p1 != p2 {
		t.Fatal("expected the same pool")
	}
	if p2.Available() != 1 {
		t.Fatal("invalidation state lost on reload")
	}
	p3 := m.Get("glm", []string{"a", "b", "c"})
	if p3 == p1 {
		t.Fatal("expected a fresh pool for a changed key set")
	}
}
