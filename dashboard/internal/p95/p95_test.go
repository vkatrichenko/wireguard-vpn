package p95

import (
	"slices"
	"testing"
)

func TestNearest(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want float64
	}{
		{
			name: "empty input returns zero",
			in:   nil,
			want: 0,
		},
		{
			name: "single value returns that value",
			in:   []float64{42},
			want: 42,
		},
		{
			name: "two values picks the larger (ceil(0.95*2)=2)",
			in:   []float64{1, 9},
			want: 9,
		},
		{
			name: "twenty values 1..20 returns 19 (ceil(0.95*20)=19)",
			in: func() []float64 {
				out := make([]float64, 20)
				for i := range out {
					out[i] = float64(i + 1)
				}
				return out
			}(),
			want: 19,
		},
		{
			name: "one hundred values 1..100 returns 95",
			in: func() []float64 {
				out := make([]float64, 100)
				for i := range out {
					out[i] = float64(i + 1)
				}
				return out
			}(),
			want: 95,
		},
		{
			name: "all-equal values returns that value",
			in:   []float64{7, 7, 7, 7, 7},
			want: 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Nearest(tc.in)
			if got != tc.want {
				t.Fatalf("Nearest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNearestUnsortedMatchesSorted(t *testing.T) {
	unsorted := []float64{3, 1, 19, 7, 14, 9, 11, 2, 17, 5, 20, 4, 6, 8, 10, 12, 13, 15, 16, 18}
	sorted := slices.Clone(unsorted)
	slices.Sort(sorted)

	gotUnsorted := Nearest(unsorted)
	gotSorted := Nearest(sorted)
	if gotUnsorted != gotSorted {
		t.Fatalf("unsorted result %v != sorted result %v", gotUnsorted, gotSorted)
	}
	if gotSorted != 19 {
		t.Fatalf("Nearest(1..20 sorted) = %v, want 19", gotSorted)
	}
}

func TestNearestDoesNotMutateInput(t *testing.T) {
	in := []float64{5, 1, 4, 2, 3}
	snapshot := slices.Clone(in)
	_ = Nearest(in)
	if !slices.Equal(in, snapshot) {
		t.Fatalf("Nearest mutated input: got %v, want %v", in, snapshot)
	}
}
