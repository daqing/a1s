package monitor

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		raw      string
		fallback time.Duration
		want     time.Duration
		wantOK   bool
	}{
		{raw: "", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: true},
		{raw: "10s", fallback: 5 * time.Second, want: 10 * time.Second, wantOK: true},
		{raw: "1m30s", fallback: 5 * time.Second, want: 90 * time.Second, wantOK: true},
		{raw: "bogus", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
		{raw: "0s", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
		{raw: "-3s", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
	}

	for _, tc := range cases {
		got, ok := parseDuration(tc.raw, tc.fallback)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("parseDuration(%q) = %s, %v; want %s, %v", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}
