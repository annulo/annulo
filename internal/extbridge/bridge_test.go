package extbridge

import (
	"errors"
	"net/url"
	"testing"
)

func TestSiteMatch(t *testing.T) {
	cases := []struct {
		pattern, url string
		want         bool
	}{
		{"https://www.doubao.com/*", "https://www.doubao.com/chat/123", true},
		{"https://www.doubao.com/*", "https://doubao.com/", false},
		{"https://www.doubao.com/*", "http://www.doubao.com/", false},
		{"https://www.doubao.com/*", "https://www.doubao.com.evil.com/", false},
		{"https://*.deepseek.com/*", "https://chat.deepseek.com/a", true},
		{"https://*.deepseek.com/*", "https://deepseek.com/", true},
		{"https://*.deepseek.com/*", "https://notdeepseek.com/", false},
		{"*://example.com/*", "http://example.com/", true},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		if got := siteMatch(c.pattern, u); got != c.want {
			t.Errorf("siteMatch(%q, %q) = %v, want %v", c.pattern, c.url, got, c.want)
		}
	}
}

func conn(b *Bridge, id string, lastActive int64, sites ...string) *Conn {
	c := &Conn{b: b, ID: id, sites: sites, lastActive: lastActive, pending: map[int64]chan reply{}, subs: map[int]func(Event){}, done: make(chan struct{})}
	b.conns[id] = c
	return c
}

func TestPick(t *testing.T) {
	b := New()
	if _, _, err := b.Pick("", "https://chat.deepseek.com/"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no connection: err = %v", err)
	}
	work := conn(b, "annulo-work", 200)
	home := conn(b, "annulo-home", 100, "https://chat.deepseek.com/*")

	// 只有一个允许了这个网站：用它，哪怕不是最近用过的
	c, need, err := b.Pick("", "https://chat.deepseek.com/a/chat")
	if err != nil || c != home || need {
		t.Fatalf("allowed in one: got %v need=%v err=%v", c, need, err)
	}
	// 都没允许：最近用过的那个，要先问用户
	c, need, _ = b.Pick("", "https://www.doubao.com/chat/")
	if c != work || !need {
		t.Fatalf("allowed in none: got %v need=%v", c, need)
	}
	// 几个都允许了：最近用过的
	work.sites = []string{"https://chat.deepseek.com/*"}
	c, _, _ = b.Pick("", "https://chat.deepseek.com/")
	if c != work {
		t.Fatalf("allowed in both: got %v", c)
	}
	// 指定了 id：就是它，没允许的要问
	c, need, _ = b.Pick("annulo-home", "https://www.doubao.com/")
	if c != home || !need {
		t.Fatalf("by id: got %v need=%v", c, need)
	}
	if _, _, err := b.Pick("annulo-gone", ""); err == nil {
		t.Fatal("unknown id should fail")
	}
	// 不带地址（ctx.browser.profile('@user')）：最近用过的
	if c, _, _ := b.Pick("", ""); c != work {
		t.Fatalf("no url: got %v", c)
	}
}

func TestErrorText(t *testing.T) {
	if got := errorText("other", "boom"); got != "boom" {
		t.Fatalf("unknown code keeps the message: %q", got)
	}
	if got := errorText("not_granted", "x"); got == "x" {
		t.Fatal("not_granted should be explained")
	}
}
