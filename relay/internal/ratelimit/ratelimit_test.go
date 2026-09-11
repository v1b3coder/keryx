package ratelimit

import (
	"testing"
	"time"
)

func TestBurstAndRefill(t *testing.T) {
	l := New(60, 120) // 60/min, burst 120
	for i := 0; i < 120; i++ {
		if !l.Allow("p") {
			t.Fatalf("burst denied at %d", i)
		}
	}
	if l.Allow("p") {
		t.Fatal("allowed beyond burst")
	}
	// After 1 second, ~1 token refilled (60/min = 1/s).
	time.Sleep(1100 * time.Millisecond)
	if !l.Allow("p") {
		t.Fatal("refill did not happen")
	}
	if l.Allow("p") {
		t.Fatal("refilled more than 1 token in ~1s")
	}
}

func TestIndependentKeys(t *testing.T) {
	l := New(1, 1)
	if !l.Allow("a") {
		t.Fatal("a denied")
	}
	if !l.Allow("b") {
		t.Fatal("b denied (keys must be independent)")
	}
	if l.Allow("a") {
		t.Fatal("a allowed twice with burst 1")
	}
}

func TestAllowNAllOrNothing(t *testing.T) {
	l := New(60, 10)
	if !l.AllowN("k", 10) {
		t.Fatal("AllowN(10) denied at full burst")
	}
	if l.AllowN("k", 2) {
		t.Fatal("AllowN(2) allowed beyond burst")
	}
	if l.Allow("k") {
		t.Fatal("single token allowed beyond burst")
	}
}

func TestCleanup(t *testing.T) {
	l := New(60, 10)
	l.Allow("old")
	l.Allow("fresh")
	// Force "old" idle by advancing time manually via a custom now func.
	base := time.Now()
	l.now = func() time.Time { return base.Add(2 * time.Hour) }
	l.Allow("fresh") // refresh fresh's lastSeen
	l.Cleanup(time.Hour)
	l.mu.Lock()
	_, hasOld := l.buckets["old"]
	_, hasFresh := l.buckets["fresh"]
	l.mu.Unlock()
	if hasOld {
		t.Fatal("idle bucket not cleaned")
	}
	if !hasFresh {
		t.Fatal("fresh bucket cleaned")
	}
}
