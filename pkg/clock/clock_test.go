package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c := NewFake(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}
	c.Advance(90 * time.Second)
	if got := c.Since(start); got != 90*time.Second {
		t.Errorf("Since(start) = %v, want 90s", got)
	}
	later := start.Add(time.Hour)
	c.Set(later)
	if got := c.Now(); !got.Equal(later) {
		t.Errorf("Now() after Set = %v, want %v", got, later)
	}
}

func TestRealMovesForward(t *testing.T) {
	c := Real()
	a := c.Now()
	if c.Since(a) < 0 {
		t.Error("real clock went backwards")
	}
}
