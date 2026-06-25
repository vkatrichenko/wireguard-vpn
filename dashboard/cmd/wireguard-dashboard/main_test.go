package main

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"53687091200", 50 << 30, false}, // plain bytes
		{"50GiB", 50 << 30, false},
		{"50 GiB", 50 << 30, false},
		{"50G", 50 << 30, false}, // single-letter treated as binary
		{"1KiB", 1024, false},
		{"1k", 1024, false},
		{"1KB", 1000, false}, // decimal suffix is power of 1000
		{"2MB", 2_000_000, false},
		{"1.5GiB", int64(1.5 * (1 << 30)), false},
		{"100b", 100, false},
		{"", 0, true},
		{"GiB", 0, true},  // no number
		{"50ZB", 0, true}, // unknown unit
		{"abc", 0, true},  // garbage
	}
	for _, c := range cases {
		got, err := parseByteSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseByteSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseByteSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
