package clock

import (
	"testing"
	"time"
)

// Spec 14 §3: QS_TIME_SCALE speeds up time for L0 tests.
func TestScaled(t *testing.T) {
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	base := NewFake(start)
	c := NewScaled(base, 60)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() at origin = %v, want %v", got, start)
	}
	base.Advance(time.Second)
	if got := c.Since(start); got != time.Minute {
		t.Errorf("after 1s of base time Since(start) = %v, want 1m", got)
	}
	if c.Scale() != 60 {
		t.Errorf("Scale() = %v", c.Scale())
	}
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		set     bool
		scaled  float64 // 0 = real clock
		wantErr bool
	}{
		{name: "unset", scaled: 0},
		{name: "empty", value: "", set: true, scaled: 0},
		{name: "one", value: "1", set: true, scaled: 0},
		{name: "hour per second", value: "3600", set: true, scaled: 3600},
		{name: "fraction", value: "2.5", set: true, scaled: 2.5},
		{name: "slower than real", value: "0.5", set: true, wantErr: true},
		{name: "not a number", value: "fast", set: true, wantErr: true},
		{name: "infinity", value: "+Inf", set: true, wantErr: true},
		{name: "nan", value: "NaN", set: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := fromLookup(func(string) (string, bool) { return tt.value, tt.set })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %T", c)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s, isScaled := c.(*Scaled)
			switch {
			case tt.scaled == 0 && isScaled:
				t.Errorf("got scaled clock, want real")
			case tt.scaled != 0 && (!isScaled || s.Scale() != tt.scaled):
				t.Errorf("got %T, want Scaled(%v)", c, tt.scaled)
			}
		})
	}
}

func TestFromEnvUsesProcessEnv(t *testing.T) {
	t.Setenv(TimeScaleEnv, "10")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := c.(*Scaled); !ok || s.Scale() != 10 {
		t.Errorf("FromEnv() = %T, want Scaled(10)", c)
	}
}
