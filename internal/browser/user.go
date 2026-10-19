package browser

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/annulo/annulo/internal/extbridge"
	"github.com/annulo/annulo/internal/i18n"
)

// 用户自己的浏览器：profile 写 @user（按网站自动选 Chrome profile）或 @user/<插件 id>（指定一个）。
// 经用户 Chrome 里的 Annulo 插件操作（internal/extbridge，docs/browser-extension.md），用的是用户已经登好的账号。
var userRe = regexp.MustCompile(`^@user(/annulo-[0-9a-f-]{36})?$`)

// IsUser：这个 profile 指的是用户自己的浏览器
func IsUser(profile string) bool { return profile == "@user" || strings.HasPrefix(profile, "@user/") }

// userTab 是用户浏览器里 Annulo 开的那个标签页
type userTab struct {
	conn *extbridge.Conn
	tab  int
	ep   *extbridge.Endpoint
}

// pickUser 选这次用哪个 Chrome profile；这个网站还没允许过就先问用户（AskGrant 时），返回选中的连接。
func (m *Manager) pickUser(o Options) (*extbridge.Conn, error) {
	if !userRe.MatchString(o.Profile) {
		return nil, i18n.Errorf("profile 名不对：%q（用户自己的浏览器写 @user）", "Invalid profile: %q (use @user for your own browser)", o.Profile)
	}
	if m.Ext == nil {
		return nil, i18n.New("这个版本的 Annulo 不能操作你自己的浏览器", "This version of Annulo can't work in your own browser")
	}
	var host string
	if o.URL != "" {
		u, err := url.Parse(o.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, i18n.Errorf("浏览器只能打开 http / https 地址：%q", "The browser can only open http / https URLs: %q", o.URL)
		}
		if m.CheckHost != nil {
			if err := m.CheckHost(u.Hostname()); err != nil {
				return nil, err
			}
		}
		host = u.Hostname()
	}
	id := strings.TrimPrefix(strings.TrimPrefix(o.Profile, "@user"), "/")
	conn, needGrant, err := m.Ext.Pick(id, o.URL)
	if errors.Is(err, extbridge.ErrNotConnected) {
		// Chrome 没开（插件只在 Chrome 开着时连得上）：替用户正常打开 Chrome，等插件连上
		if LaunchUserChrome() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if m.Ext.Wait(ctx) {
				conn, needGrant, err = m.Ext.Pick(id, o.URL)
			}
			cancel()
		}
		if errors.Is(err, extbridge.ErrNotConnected) {
			return nil, i18n.New("没连上你的 Chrome：在 Chrome 里装上 Annulo 插件并打开 Chrome，再试一次", "Couldn't reach your Chrome: install the Annulo extension in Chrome, keep Chrome open, and try again")
		}
	}
	if err != nil {
		return nil, err
	}
	if needGrant {
		if !o.AskGrant {
			return nil, i18n.Errorf("还没允许 Annulo 在 %s 上操作你的浏览器：先在页面上运行一次，在弹出的窗口里点「允许」", "Annulo isn't allowed on %s in your browser yet: run this once from a page and click Allow in the window that pops up", host)
		}
		// 没指定哪个 Chrome profile：每个连着的都问，用户在登录了这个网站的那个里点允许，就用那个
		ask := []*extbridge.Conn{conn}
		if id == "" {
			ask = m.Ext.Conns()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		granted, err := extbridge.Grant(ctx, ask, o.URL, o.Why)
		cancel()
		if err != nil {
			return nil, err
		}
		if granted == nil {
			return nil, i18n.Errorf("你没有允许 Annulo 在 %s 上操作", "You didn't allow Annulo on %s", host)
		}
		conn = granted
	}
	return conn, nil
}

// openUser 在用户的 Chrome 里开一个窗口，用假浏览器端点接上 chromedp，之后和独立 Chrome 一样操作（setup）。
func (m *Manager) openUser(o Options) (*Page, error) {
	conn, err := m.pickUser(o)
	if err != nil {
		return nil, err
	}
	key := "@user/" + conn.ID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	tab, window, err := conn.Open(ctx, "about:blank", o.Show)
	cancel()
	if err != nil {
		return nil, i18n.Errorf("在你的浏览器里打开窗口失败：%v", "Failed to open a window in your browser: %v", err)
	}
	ep, err := conn.Endpoint(tab, window)
	if err != nil {
		conn.Close(tab)
		return nil, err
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), ep.URL, chromedp.NoModifyURL)
	pctx, pcancel := chromedp.NewContext(allocCtx)
	p := &Page{m: m, profile: key, ctx: pctx, cancel: pcancel, user: &userTab{conn: conn, tab: tab, ep: ep}, pending: map[network.RequestID]*Response{}, wake: make(chan struct{})}
	p.alloc = func() {
		allocCancel()
		ep.Close()
		ep.CloseTab() // 一般 chromedp 已经关过（Target.closeTarget）；没关上的这里补一次，留给用户的交出去
	}
	return m.setup(p, pctx, o)
}

// userProfileID 是 ctx.browser.profile('@user') 的答案：选中的 Chrome profile 里插件的 id
func (m *Manager) userProfileID(profile string) (string, error) {
	if !userRe.MatchString(profile) {
		return "", i18n.Errorf("profile 名不对：%q（用户自己的浏览器写 @user）", "Invalid profile: %q (use @user for your own browser)", profile)
	}
	if m.Ext == nil {
		return "", nil
	}
	conn, _, err := m.Ext.Pick(strings.TrimPrefix(strings.TrimPrefix(profile, "@user"), "/"), "")
	if err != nil {
		return "", nil
	}
	return conn.ID, nil
}

// LaunchUserChrome 正常打开用户的 Chrome（不带任何参数，用户平时的 profile），不抢前台
func LaunchUserChrome() error {
	exe := FindChrome()
	if exe == "" {
		return errors.New("chrome not found")
	}
	if runtime.GOOS == "darwin" {
		app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		return exec.Command("open", "-g", "-a", app).Run()
	}
	return exec.Command(exe).Start()
}
