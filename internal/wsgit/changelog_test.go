package wsgit

import (
	"testing"

	"github.com/annulo/annulo/internal/i18n"
)

func TestChangelog(t *testing.T) {
	log := parseChangelog([]byte(`
- version: 0.9.0
  zh: 中文说明
  en: English note
- version: v0.8.0
  ja: 日本語
- version: 1.10
  zh: 不是 semver，跳过
`), SemverNo)
	if len(log) != 2 {
		t.Fatalf("got %v", log)
	}
	vs := []Version{{No: 9000, Note: "tag"}, {No: 8000, Note: "tag"}, {No: 7000, Note: "tag"}}
	i18n.SetLocale(func() string { return "en" })
	defer i18n.SetLocale(nil)
	localizeNotes(vs, log)
	if vs[0].Note != "English note" || vs[1].Note != "日本語" || vs[2].Note != "tag" {
		t.Fatalf("got %+v", vs)
	}
	i18n.SetLocale(func() string { return "zh" })
	vs[0].Note = ""
	localizeNotes(vs, log)
	if vs[0].Note != "中文说明" {
		t.Fatalf("got %+v", vs[0])
	}
}
