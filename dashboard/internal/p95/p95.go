// Package p95 computes the 95th percentile of a sample using the
// nearest-rank method: sort ascending and pick the value at the
// 1-indexed position ceil(0.95 * n).
//
// Nearest-rank (no interpolation) is the variant most monitoring tools
// display, and it stays predictable on small samples — every output is
// an actual observed value, never a synthesised midpoint. That matters
// for the dashboard's per-client p95 throughput cell, which often runs
// on short windows where interpolated percentiles can drift in
// counter-intuitive ways.
package p95

import (
	"math"
	"sort"
)

// Nearest returns the 95th-percentile value of rates using the
// nearest-rank rule. The input is not mutated — Nearest sorts a copy.
// An empty input returns 0.
func Nearest(rates []float64) float64 {
	n := len(rates)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, rates)
	sort.Float64s(sorted)
	idx := int(math.Ceil(0.95*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
