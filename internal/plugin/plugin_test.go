package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		in       string
		id, rest string
		ok       bool
	}{
		{"x", "", "x", true},
		{"social/x", "social", "x", true},
		{"Social/x", "", "", false},
		{"social_a/x", "", "", false},
		{"social/", "", "", false},
		{"social/x/y", "", "", false},
	} {
		id, rest, ok := Split(c.in)
		if id != c.id || rest != c.rest || ok != c.ok {
			t.Errorf("Split(%q) = %q %q %v", c.in, id, rest, ok)
		}
	}
	if Name("social", "x") != "social/x" || Name("", "x") != "x" {
		t.Error("Name")
	}
	if ChatKey("social/write-x") != "social__write-x" {
		t.Error("ChatKey")
	}
	if TableKey("social", "posts") != "social_posts" || TableKey("", "posts") != "posts" {
		t.Error("TableKey")
	}
}

func TestIDs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"social", "crm", "bad_id", "nometa"} {
		os.MkdirAll(filepath.Join(root, Dir, d), 0o755)
		if d != "nometa" {
			os.WriteFile(filepath.Join(root, Dir, d, MetaFile), []byte("{}"), 0o644)
		}
	}
	if got := IDs(root); !reflect.DeepEqual(got, []string{"crm", "social"}) {
		t.Errorf("IDs = %v", got)
	}
	if got := Sources(root); !reflect.DeepEqual(got, []string{"", "crm", "social"}) {
		t.Errorf("Sources = %v", got)
	}
	if IDs(t.TempDir()) != nil {
		t.Error("no plugins dir")
	}
}
