package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// browserHost 把 internal/browser 接到本机函数的 ctx.browser 上：打开的地址和 fetch 同一套出网规则（不许本机、内网）。
// run 不为空时是页面上发起的一次运行（local/run）：这次打开的浏览器记在 run 上，页面能让它显示到前台（local/runs/<id>/show）；
// run.offscreen 时本来要无界面跑的改用「可看」的真窗口（macOS 后台启动、留在别的窗口后面；Windows 放屏幕外），才调得出来
// （页面在 local/run 里要求：后台发布、手动自检、采集、登录）。用户点「打开看」时还没打开浏览器（run.wantShow）：一打开就显示。
// project 不为空时浏览器按项目分开：profile 名前面加上项目（~/.annulo/browser/<项目>/<名字>），
// 同一个名字（比如 facebook-1）在两个项目里是两个浏览器，A 项目登的账号不会出现在 B 项目里。
type browserHost struct {
	m       *browser.Manager
	run     *localRun
	project string
}

func (b browserHost) profile(name string) (string, error) {
	if browser.IsUser(name) {
		return name, nil // 用户自己的浏览器：不按项目分开（browser.Manager 自己校验写法）
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return "", i18n.Errorf("profile 名不对：%q（只能用小写字母、数字、- 和 _）", "Invalid profile name: %q (use lowercase letters, digits, - and _)", name)
	}
	if b.project == "" {
		return name, nil
	}
	return b.project + "/" + name, nil
}

func (s *Server) CloseBrowsers() { s.browser.CloseKept() }

func (b browserHost) Open(_ context.Context, o localfn.BrowserOptions) (localfn.BrowserPage, error) {
	if b.run != nil && b.run.offscreen && !o.Show {
		o.Offscreen = true
	}
	profile, err := b.profile(o.Profile)
	if err != nil {
		return nil, err
	}
	// 用户自己的浏览器碰到没允许过的网站：页面上点的运行（有人看着）弹窗问用户，后台定时任务直接报错
	why := ""
	if b.run != nil {
		why = b.run.fn
	}
	p, release, err := b.m.Acquire(browser.Options{Profile: profile, URL: o.URL, Show: o.Show, Offscreen: o.Offscreen, KeepOpen: o.KeepOpen, AskGrant: b.run != nil, Why: why})
	if err != nil {
		return nil, err
	}
	unlist := func() {}
	if b.run != nil {
		b.run.addPage(p)
		unlist = func() { b.run.removePage(p) }
		inner := release
		release = func() { unlist(); inner() }
		// 用户点「打开看」时浏览器还没打开：现在打开了，显示出来
		if b.run.wantsShow() {
			go p.Reveal(context.Background())
		}
	}
	return &browserLease{Page: p, m: b.m, release: release, unlist: unlist}, nil
}

// UserBrowsers 是 ctx.browser.user() 的 profiles：用户 Chrome 里连着的 Annulo 插件
func (b browserHost) UserBrowsers() any {
	if b.m.Ext == nil {
		return []any{}
	}
	// 按 json 字段名给 JS（id、name、email、sites、last_active）
	var out []any
	if raw, err := json.Marshal(b.m.Ext.Profiles()); err == nil {
		json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = []any{}
	}
	return out
}

func (b browserHost) ProfileID(name string) (string, error) {
	profile, err := b.profile(name)
	if err != nil {
		return "", err
	}
	return b.m.ProfileID(profile)
}

type browserLease struct {
	*browser.Page
	m       *browser.Manager
	release func()
	unlist  func()
}

func (p *browserLease) Close() error { p.release(); return nil }

// KeepForUser：本机函数失败时，用户正看着的窗口留着不关（browser.Manager.Keep），好看卡在哪一步
func (p *browserLease) KeepForUser() bool {
	if !p.m.Keep(p.Page) {
		return false
	}
	p.unlist()
	return true
}
