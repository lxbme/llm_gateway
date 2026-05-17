package pool

import (
	"math"
	"testing"
	"time"
)

func TestStats_InFlightAccounting(t *testing.T) {
	s := &endpointStats{}
	if s.InFlight.Load() != 0 {
		t.Fatalf("expected 0 in_flight, got %d", s.InFlight.Load())
	}
	starts := make([]time.Time, 5)
	for i := range 5 {
		starts[i] = s.start()
	}
	if got := s.InFlight.Load(); got != 5 {
		t.Fatalf("expected 5 in_flight, got %d", got)
	}
	for _, t0 := range starts {
		s.end(t0, false)
	}
	if got := s.InFlight.Load(); got != 0 {
		t.Fatalf("expected 0 after end, got %d", got)
	}
	if got := s.Success.Load(); got != 5 {
		t.Fatalf("expected 5 successes, got %d", got)
	}
	if s.Failure.Load() != 0 {
		t.Fatalf("expected 0 failures, got %d", s.Failure.Load())
	}
}

func TestStats_FailureRecorded(t *testing.T) {
	s := &endpointStats{}
	t0 := s.start()
	s.end(t0, true)
	if s.Failure.Load() != 1 || s.Success.Load() != 0 {
		t.Fatalf("expected 1 failure 0 success, got fail=%d succ=%d", s.Failure.Load(), s.Success.Load())
	}
}

// TestStats_EWMA_Convergence feeds a series of samples and verifies the EWMA
// shifts toward the most recent observation at the configured alpha=0.2.
func TestStats_EWMA_Convergence(t *testing.T) {
	s := &endpointStats{}
	// First sample initializes EWMA.
	s.observeLatency(1000)
	if got := s.LatencyUsEWMA.Load(); got != 1000 {
		t.Fatalf("first sample should init EWMA to 1000, got %d", got)
	}
	// Steady-state at 1000 stays at 1000.
	s.observeLatency(1000)
	s.observeLatency(1000)
	if got := s.LatencyUsEWMA.Load(); got != 1000 {
		t.Fatalf("steady-state stuck at 1000, got %d", got)
	}
	// New sample 100; alpha=0.2 => 0.2*100 + 0.8*1000 = 820.
	s.observeLatency(100)
	if got := s.LatencyUsEWMA.Load(); math.Abs(float64(got)-820) > 1 {
		t.Fatalf("expected ~820, got %d", got)
	}
}

func TestStats_SnapshotReportsRate(t *testing.T) {
	s := &endpointStats{}
	t0 := s.start()
	s.end(t0, false)
	t1 := s.start()
	s.end(t1, true)
	t2 := s.start()
	s.end(t2, false)
	in, succ, fail, rate, _ := s.snapshot()
	if in != 0 || succ != 2 || fail != 1 {
		t.Fatalf("counters off: in=%d succ=%d fail=%d", in, succ, fail)
	}
	if math.Abs(rate-2.0/3.0) > 1e-6 {
		t.Fatalf("rate=%.6f want 0.6667", rate)
	}
}
