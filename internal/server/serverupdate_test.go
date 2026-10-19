package server

import "testing"

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"2026.10.15", "2026.10.14-2", true},
		{"2026.10.15", "2026.10.15", false},
		{"2026.10.15", "2026.10.15+next", false}, // 比发布的那版还新一点
		{"2026.10.15-1", "2026.10.15", true},
		{"2026.10.15", "2026.10.15-1", false},
		{"2026.11.1", "2026.10.30", true},
		{"2026.10.15", "dev", false},
		{"2026.10.15", "4a76ab0", false},
	} {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("%s vs %s → %v，要 %v", c.latest, c.current, got, c.want)
		}
	}
}
