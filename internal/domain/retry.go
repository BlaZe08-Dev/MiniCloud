package domain

import (
	"math"
	"time"
)

// RetryPolicy holds the job-retry backoff parameters of ADR-0015.
type RetryPolicy struct {
	BaseBackoff        time.Duration
	MaxBackoff         time.Duration
	JitterMin          float64
	JitterMax          float64
	DefaultMaxAttempts int
}

// DefaultRetryPolicy returns the accepted ADR-0015 parameters: base 1 s, cap 30 s, jitter factor
// in [0.5, 1.5], default max_attempts 3.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		BaseBackoff:        time.Second,
		MaxBackoff:         30 * time.Second,
		JitterMin:          0.5,
		JitterMax:          1.5,
		DefaultMaxAttempts: 3,
	}
}

// BackoffFunc maps the number of the attempt that just failed to the delay before the job is
// eligible for re-dispatch.
type BackoffFunc func(attemptNo int) time.Duration

// BackoffFunc binds the policy and a random source into a BackoffFunc.
func (p RetryPolicy) BackoffFunc(rnd func() float64) BackoffFunc {
	return func(attemptNo int) time.Duration { return ComputeBackoff(p, attemptNo, rnd) }
}

// ComputeBackoff returns min(MaxBackoff, BaseBackoff * 2^(attemptNo-1)) * jitter, where jitter is
// drawn uniformly from [JitterMin, JitterMax] using rnd, a source of values in [0, 1). The cap
// applies to the exponential term before the jitter, as ADR-0015 writes the formula, so the longest
// possible delay is MaxBackoff * JitterMax (45 s by default).
//
// The function is pure: all randomness comes from rnd. Values outside [0, 1] are clamped to the
// ends of the range, NaN and a nil rnd give the neutral midpoint factor, so a faulty source can
// never push the result out of [JitterMin, JitterMax]. An attemptNo below 1 is treated as 1. A
// policy with a non-positive base or cap yields 0.
func ComputeBackoff(p RetryPolicy, attemptNo int, rnd func() float64) time.Duration {
	if p.BaseBackoff <= 0 || p.MaxBackoff <= 0 {
		return 0
	}
	if attemptNo < 1 {
		attemptNo = 1
	}

	exp := p.BaseBackoff
	for i := 1; i < attemptNo && exp < p.MaxBackoff; i++ {
		if exp > p.MaxBackoff/2 {
			exp = p.MaxBackoff
			break
		}
		exp *= 2
	}
	if exp > p.MaxBackoff {
		exp = p.MaxBackoff
	}

	factor := (p.JitterMin + p.JitterMax) / 2
	if rnd != nil {
		if u := rnd(); !math.IsNaN(u) {
			if u < 0 {
				u = 0
			} else if u > 1 {
				u = 1
			}
			factor = p.JitterMin + u*(p.JitterMax-p.JitterMin)
		}
	}
	return time.Duration(float64(exp) * factor)
}
