package server

import (
	"context"

	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/localfn"
)

// browserHost 把 internal/browser 接到本机函数的 ctx.browser 上：打开的地址和 fetch 同一套出网规则（不许本机、内网）。
type browserHost struct{ m *browser.Manager }

func (s *Server) CloseBrowsers() { s.browser.CloseKept() }

func (b browserHost) Open(_ context.Context, o localfn.BrowserOptions) (localfn.BrowserPage, error) {
	p, release, err := b.m.Acquire(browser.Options{Profile: o.Profile, URL: o.URL, Show: o.Show, Offscreen: o.Offscreen, KeepOpen: o.KeepOpen})
	if err != nil {
		return nil, err
	}
	return &browserLease{Page: p, release: release}, nil
}

func (b browserHost) ProfileID(profile string) (string, error) { return b.m.ProfileID(profile) }

type browserLease struct {
	*browser.Page
	release func()
}

func (p *browserLease) Close() error { p.release(); return nil }
