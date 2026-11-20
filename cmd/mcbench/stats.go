// Command mcbench measures MiniCloud over its real HTTP API (ADR-0021).
package main

import (
	"encoding/json"
	"math"
	"sort"
)

// Stats summarises one sample. Percentiles use the nearest-rank definition on the sorted
// sample. For N < 20 the nearest-rank p95 equals the maximum; P95IsMax says so.
type Stats struct {
	N        int     `json:"n"`
	Min      float64 `json:"min"`
	Median   float64 `json:"median"`
	P95      float64 `json:"p95"`
	Max      float64 `json:"max"`
	P95IsMax bool    `json:"p95_is_max"`
}

// MarshalJSON omits every summary number when N is 0 (there is nothing to summarise), so a
// reader cannot mistake an empty series for a 0 ms one. The record then carries only "n": 0.
func (s Stats) MarshalJSON() ([]byte, error) {
	if s.N == 0 {
		return []byte(`{"n":0}`), nil
	}
	type plain Stats
	return json.Marshal(plain(s))
}

// Percentile returns the nearest-rank percentile (0 < p <= 100) of an already sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Median is the middle value, or the mean of the two middle values for even N.
func Median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Summarize never mutates its input and never drops or trims a value.
func Summarize(values []float64) Stats {
	if len(values) == 0 {
		return Stats{}
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	p95 := Percentile(s, 95)
	return Stats{N: len(s), Min: s[0], Median: Median(s), P95: p95, Max: s[len(s)-1], P95IsMax: p95 == s[len(s)-1]}
}

func sortStrings(s []string) { sort.Strings(s) }
