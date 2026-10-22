package clock

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// TimeScaleEnv makes time run faster in L0 end-to-end tests (spec 14 §3),
// e.g. QS_TIME_SCALE=3600 turns an hour-long grace period into a second.
// It must never be set in production.
const TimeScaleEnv = "QS_TIME_SCALE"

// Scaled is a clock that runs `scale` times faster than its base clock,
// starting from the moment it was created.
type Scaled struct {
	base   Clock
	scale  float64
	origin time.Time
}

// NewScaled returns a clock running scale times faster than base. scale must
// be >= 1.
func NewScaled(base Clock, scale float64) *Scaled {
	return &Scaled{base: base, scale: scale, origin: base.Now()}
}

// Now returns origin + elapsed*scale.
func (s *Scaled) Now() time.Time {
	elapsed := s.base.Since(s.origin)
	return s.origin.Add(time.Duration(float64(elapsed) * s.scale))
}

// Since returns the scaled time elapsed since t.
func (s *Scaled) Since(t time.Time) time.Duration { return s.Now().Sub(t) }

// Scale returns the speed-up factor.
func (s *Scaled) Scale() float64 { return s.scale }

// FromEnv returns the real clock, or a Scaled clock when QS_TIME_SCALE is set
// to a value greater than 1.
func FromEnv() (Clock, error) {
	return fromLookup(os.LookupEnv)
}

func fromLookup(lookup func(string) (string, bool)) (Clock, error) {
	v, ok := lookup(TimeScaleEnv)
	if !ok || v == "" {
		return Real(), nil
	}
	scale, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(scale) || math.IsInf(scale, 0) || scale < 1 {
		return nil, fmt.Errorf("%s=%q: must be a number >= 1", TimeScaleEnv, v)
	}
	if scale == 1 {
		return Real(), nil
	}
	return NewScaled(Real(), scale), nil
}
