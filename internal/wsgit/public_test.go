package wsgit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 同一份模板复制到另一个集群：版本号从 1 重新数，changelog 按标签（v135）对；没标签的老版本按版本号对。
func TestVersionsPublicChangelogByTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/publish/state"):
			json.NewEncoder(w).Encode(map[string]any{"versions": []map[string]any{
				{"version_no": 1, "note": "copied", "created_at": "2026-10-01T00:00:00Z"},
				{"version_no": 2, "tag": "v135", "note": "raw note", "created_at": "2026-10-02T00:00:00Z"},
			}})
		case strings.HasSuffix(r.URL.Path, "/file"):
			json.NewEncoder(w).Encode(map[string]any{"body": "- version: 135\n  en: tagged\n- version: 2\n  en: wrong\n- version: 1\n  en: first\n"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := templateHost
	templateHost = func(Template) string { return srv.URL }
	defer func() { templateHost = old }()
	t.Setenv("ANNULO_DIR", t.TempDir())

	vs, err := versionsPublic(context.Background(), Template{Site: "p/s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].No != 2 || vs[0].Label != "v135" || vs[0].Note != "tagged" {
		t.Fatalf("tagged version = %+v", vs)
	}
	if vs[1].Label != "" || vs[1].Note != "first" {
		t.Fatalf("untagged version = %+v", vs[1])
	}
}
