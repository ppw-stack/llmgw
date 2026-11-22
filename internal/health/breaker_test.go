package health

import (
	"testing"
	"time"
)

func TestBreakerOpensAfterTheThreshold(t *testing.T) {
	b := NewBreaker()
	for i := 0; i < 2; i++ {
		if !b.Allow("a") {
			t.Fatal("instance must be usable before the threshold")
		}
		b.RecordFailure("a", 3, 30*time.Second)
	}
	if !b.Allow("a") {
		t.Fatal("instance must still be usable below the threshold")
	}
	b.RecordFailure("a", 3, 30*time.Second)
	if b.Allow("a") {
		t.Fatal("instance must be skipped once the threshold is reached")
	}
	if !b.Open("a") {
		t.Fatal("Open must report the open circuit")
	}
}

func TestBreakerRecoversAfterTheCooldown(t *testing.T) {
	b := NewBreaker()
	now := time.Now()
	b.now = func() time.Time { return now }
	b.RecordFailure("a", 1, 30*time.Second)
	if b.Allow("a") {
		t.Fatal("instance must be skipped right after the failure")
	}
	now = now.Add(31 * time.Second)
	if !b.Allow("a") {
		t.Fatal("instance must be usable again after the cooldown")
	}
}

func TestSuccessClearsTheCounter(t *testing.T) {
	b := NewBreaker()
	b.RecordFailure("a", 2, 30*time.Second)
	b.RecordSuccess("a")
	b.RecordFailure("a", 2, 30*time.Second)
	if !b.Allow("a") {
		t.Fatal("the counter must restart after a success")
	}
}

func TestBreakerTracksInstancesSeparately(t *testing.T) {
	b := NewBreaker()
	b.RecordFailure("a", 1, 30*time.Second)
	if b.Allow("a") {
		t.Fatal("instance a must be skipped")
	}
	if !b.Allow("b") {
		t.Fatal("instance b must stay usable")
	}
}
