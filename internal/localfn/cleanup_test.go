package localfn

import "testing"

// keepPage 是测试用的浏览器：只实现 Close 和 KeepForUser
type keepPage struct {
	BrowserPage
	watched      bool
	closed, kept bool
}

func (p *keepPage) Close() error { p.closed = true; return nil }
func (p *keepPage) KeepForUser() bool {
	p.kept = p.watched
	return p.watched
}

// 失败时用户正看着的窗口留着，别的照样关；成功时全关
func TestCleanupKeepsWatchedWindowOnFailure(t *testing.T) {
	var logs int
	emit := func(e Event) { logs++ }
	watched, hidden := &keepPage{watched: true}, &keepPage{}
	r := &runner{emit: emit, pages: []BrowserPage{watched, hidden}}
	r.cleanup(true)
	if watched.closed || !watched.kept || !hidden.closed || logs != 1 {
		t.Fatalf("failed run: watched=%+v hidden=%+v logs=%d", *watched, *hidden, logs)
	}
	ok := &keepPage{watched: true}
	r = &runner{emit: emit, pages: []BrowserPage{ok}}
	r.cleanup(false)
	if !ok.closed || ok.kept {
		t.Fatalf("successful run should close: %+v", *ok)
	}
}
