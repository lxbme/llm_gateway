package pool

import (
	"math"
	"sync/atomic"
	"time"
)

const ewmaAlphaPercent uint64 = 20 // alpha=0.2 expressed as a percentage to keep math integer-friendly

type endpointStats struct {
	InFlight       atomic.Int64
	Success        atomic.Uint64
	Failure        atomic.Uint64
	LatencyUsEWMA  atomic.Uint64 // microseconds; 0 means "no samples yet"
}

func (s *endpointStats) start() time.Time {
	s.InFlight.Add(1)
	return time.Now()
}

func (s *endpointStats) end(startedAt time.Time, errored bool) {
	s.InFlight.Add(-1)
	if errored {
		s.Failure.Add(1)
	} else {
		s.Success.Add(1)
	}
	dur := max(time.Since(startedAt), 0)
	s.observeLatency(uint64(dur.Microseconds()))
}

// observeLatency updates the EWMA: new = alpha * sample + (1-alpha) * old.
// First sample (old == 0) initializes EWMA to the sample value.
func (s *endpointStats) observeLatency(sampleUs uint64) {
	for {
		old := s.LatencyUsEWMA.Load()
		var next uint64
		if old == 0 {
			next = sampleUs
		} else {
			next = (ewmaAlphaPercent*sampleUs + (100-ewmaAlphaPercent)*old) / 100
		}
		if s.LatencyUsEWMA.CompareAndSwap(old, next) {
			return
		}
	}
}

func (s *endpointStats) snapshot() (int64, uint64, uint64, float64, float64) {
	in := s.InFlight.Load()
	succ := s.Success.Load()
	fail := s.Failure.Load()
	total := succ + fail
	rate := 0.0
	if total > 0 {
		rate = float64(succ) / float64(total)
	}
	latMs := float64(s.LatencyUsEWMA.Load()) / 1000.0
	if math.IsNaN(latMs) {
		latMs = 0
	}
	return in, succ, fail, rate, latMs
}
