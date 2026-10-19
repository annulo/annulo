package browser

import (
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestFixMojibake(t *testing.T) {
	want := `data: {"text":"2026 年有哪些好用的外贸建站工具？"}`
	garbled, _ := charmap.Windows1252.NewDecoder().String(want)
	if garbled == want {
		t.Fatal("test setup: expected garbling")
	}
	if got := fixMojibake(garbled); got != want {
		t.Fatalf("fixMojibake = %q", got)
	}
	for _, s := range []string{"plain ascii", "已经是正常的中文", "café au lait", ""} {
		if got := fixMojibake(s); got != s {
			t.Errorf("fixMojibake(%q) changed it to %q", s, got)
		}
	}
}
