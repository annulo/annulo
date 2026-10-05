package server

import (
	"net/http/httptest"
	"testing"
)

func TestIsPageRequest(t *testing.T) {
	cases := []struct {
		path, dest, accept string
		want               bool
	}{
		{"/", "iframe", "text/html", true},
		{"/RouteTest", "iframe", "text/html", true},
		{"/blog/a", "empty", "text/html,application/xhtml+xml", true}, // 经 service worker 转手
		{"/", "empty", "*/*", false},                                  // fetch
		{"/api/x", "iframe", "text/html", false},
		{"/func/x", "iframe", "text/html", false},
		{"/index.md", "iframe", "text/html", false},
		{"/_client/app_1.js", "script", "*/*", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Header.Set("Sec-Fetch-Dest", c.dest)
		r.Header.Set("Accept", c.accept)
		if got := isPageRequest(r); got != c.want {
			t.Errorf("%s dest=%s accept=%s: %v", c.path, c.dest, c.accept, got)
		}
	}
}
