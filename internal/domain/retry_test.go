package domain

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func fixedRand(v float64) func() float64 { return func() float64 { return v } }

func TestDomain_DefaultRetryPolicy_Values(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.BaseBackoff != time.Second || p.MaxBackoff != 30*time.Second {
		t.Fatalf("base/cap = %v/%v, want 1s/30s (ADR-0015)", p.BaseBackoff, p.MaxBackoff)
	}
	if p.JitterMin != 0.5 || p.JitterMax != 1.5 {
		t.Fatalf("jitter = [%v, %v], want [0.5, 1.5] (ADR-0015)", p.JitterMin, p.JitterMax)
	}
	if p.DefaultMaxAttempts != 3 {
		t.Fatalf("DefaultMaxAttempts = %d, want 3", p.DefaultMaxAttempts)
	}
}

// TestDomain_ComputeBackoff_FullJitter pins ADR-0015: backoff = min(30s, 1s * 2^(attempt-1)) * jitter
// with jitter in [0.5, 1.5]. The cap applies to the exponential term before the jitter, so the
// largest possible delay is 45s. Exact values at the two ends and the middle of the random range
// pin the jitter bounds; a sweep over many random draws proves nothing escapes them; late attempts
// pin the 30s cap.
func TestDomain_ComputeBackoff_FullJitter(t *testing.T) {
	p := DefaultRetryPolicy()

	exact := []struct {
		name      string
		attemptNo int
		rnd       float64
		want      time.Duration
	}{
		{"attempt1_low", 1, 0, 500 * time.Millisecond},
		{"attempt1_mid", 1, 0.5, time.Second},
		{"attempt2_low", 2, 0, time.Second},
		{"attempt2_mid", 2, 0.5, 2 * time.Second},
		{"attempt3_mid", 3, 0.5, 4 * time.Second},
		{"attempt4_mid", 4, 0.5, 8 * time.Second},
		{"attempt5_mid", 5, 0.5, 16 * time.Second},
		{"attempt6_capped_mid", 6, 0.5, 30 * time.Second},    // 32s exponential, capped at 30s
		{"attempt6_capped_low", 6, 0, 15 * time.Second},      // 0.5 * 30s
		{"attempt10_capped_low", 10, 0, 15 * time.Second},    // uncapped would be 256s
		{"attempt20_capped_mid", 20, 0.5, 30 * time.Second},  // uncapped would be 524288s
		{"attempt20_capped_high", 20, 1, 45 * time.Second},   // 1.5 * 30s: the documented maximum
		{"attempt1_high", 1, 1, 1500 * time.Millisecond},     // rnd 1 clamps to the top of the range
		{"attempt3_just_below_one", 3, 0.999999, 5999996000}, // 4s * (0.5 + 0.999999) in ns
	}
	for _, tc := range exact {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeBackoff(p, tc.attemptNo, fixedRand(tc.rnd))
			// Allow 1 microsecond of float rounding.
			if diff := got - tc.want; diff < -time.Microsecond || diff > time.Microsecond {
				t.Fatalf("ComputeBackoff(attempt %d, rnd %v) = %v, want %v", tc.attemptNo, tc.rnd, got, tc.want)
			}
		})
	}

	t.Run("always_within_jitter_bounds_of_capped_exponential", func(t *testing.T) {
		src := rand.New(rand.NewSource(7))
		for attemptNo := 1; attemptNo <= 25; attemptNo++ {
			exp := time.Second * time.Duration(math.Pow(2, float64(attemptNo-1)))
			if exp > 30*time.Second || exp <= 0 {
				exp = 30 * time.Second
			}
			lo := time.Duration(0.5 * float64(exp))
			hi := time.Duration(1.5 * float64(exp))
			for i := 0; i < 500; i++ {
				got := ComputeBackoff(p, attemptNo, src.Float64)
				if got < lo-time.Microsecond || got > hi+time.Microsecond {
					t.Fatalf("attempt %d draw %d: backoff %v outside [%v, %v]", attemptNo, i, got, lo, hi)
				}
			}
		}
	})

	t.Run("never_exceeds_45s_even_for_huge_attempt_numbers", func(t *testing.T) {
		for _, n := range []int{21, 63, 64, 1000, math.MaxInt32} {
			if got := ComputeBackoff(p, n, fixedRand(1)); got != 45*time.Second {
				t.Fatalf("attempt %d at rnd 1 = %v, want 45s (cap 30s * 1.5, no overflow)", n, got)
			}
		}
	})
}

func TestDomain_ComputeBackoff_NonPositiveAttemptTreatedAsFirst(t *testing.T) {
	p := DefaultRetryPolicy()
	want := ComputeBackoff(p, 1, fixedRand(0.5))
	for _, n := range []int{0, -1, math.MinInt32} {
		if got := ComputeBackoff(p, n, fixedRand(0.5)); got != want {
			t.Fatalf("attempt %d = %v, want %v (same as attempt 1)", n, got, want)
		}
	}
}

func TestDomain_ComputeBackoff_RandomSourceIsClampedAndOptional(t *testing.T) {
	p := DefaultRetryPolicy()
	if got := ComputeBackoff(p, 2, fixedRand(-5)); got != time.Second {
		t.Fatalf("rnd -5 = %v, want the bottom of the range (0.5 * 2s = 1s)", got)
	}
	if got := ComputeBackoff(p, 2, fixedRand(7)); got != 3*time.Second {
		t.Fatalf("rnd 7 = %v, want the top of the range (1.5 * 2s = 3s)", got)
	}
	if got := ComputeBackoff(p, 2, fixedRand(math.NaN())); got != 2*time.Second {
		t.Fatalf("rnd NaN = %v, want the neutral factor 1.0 (2s)", got)
	}
	if got := ComputeBackoff(p, 2, nil); got != 2*time.Second {
		t.Fatalf("nil random source = %v, want the neutral factor 1.0 (2s)", got)
	}
}

func TestDomain_ComputeBackoff_DeterministicForSameInputs(t *testing.T) {
	p := DefaultRetryPolicy()
	a := ComputeBackoff(p, 4, rand.New(rand.NewSource(42)).Float64)
	b := ComputeBackoff(p, 4, rand.New(rand.NewSource(42)).Float64)
	if a != b {
		t.Fatalf("same seed gave %v and %v", a, b)
	}
}

func TestDomain_ComputeBackoff_InvalidPolicyYieldsZero(t *testing.T) {
	if got := ComputeBackoff(RetryPolicy{}, 3, fixedRand(0.5)); got != 0 {
		t.Fatalf("zero policy = %v, want 0", got)
	}
}

func TestDomain_RetryPolicy_BackoffFuncBindsPolicyAndSource(t *testing.T) {
	p := DefaultRetryPolicy()
	f := p.BackoffFunc(fixedRand(0.5))
	if got := f(3); got != 4*time.Second {
		t.Fatalf("BackoffFunc(3) = %v, want 4s", got)
	}
}
