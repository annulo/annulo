package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/creght"
)

// Annulo 订阅（docs/billing.md）：就是 creght 账号的会员。Annulo 只读平台的权益接口、原样显示，不在客户端判断；
// 买、付钱、管理都在 creght 网页上（subscribe_url，用系统浏览器打开）。没连 creght、或者账号不在内测名单（ErrPreview）时整节不出现。

type entCache struct {
	mu   sync.Mutex
	host string
	at   time.Time
	v    *creght.Entitlements
	err  error
}

// entitlements 读权益，按集群缓存 1 分钟；refresh 跳过缓存（开通回来、窗口拿回焦点时）。
func (s *Server) entitlements(ctx context.Context, refresh bool) (*creght.Entitlements, error) {
	host := s.loginHost()
	c := &s.ent
	c.mu.Lock()
	if !refresh && c.host == host && time.Since(c.at) < time.Minute {
		v, err := c.v, c.err
		c.mu.Unlock()
		return v, err
	}
	c.mu.Unlock()
	v, err := creght.NewClient(host).Entitlements(ctx)
	c.mu.Lock()
	c.host, c.at, c.v, c.err = host, time.Now(), v, err
	c.mu.Unlock()
	return v, err
}

// cloudAvailable：连着 creght、这个账号能用 Annulo 的云服务（不在内测名单的是 false）。
func (s *Server) cloudAvailable(ctx context.Context) bool {
	if !s.signedIn() {
		return false
	}
	_, err := s.entitlements(ctx, false)
	return !errors.Is(err, creght.ErrPreview) && !errors.Is(err, creght.ErrNotLoggedIn)
}

// GET settings/subscription[?refresh=1]：{ available, entitlements?, error? }。available 是 false 时界面不显示这一节。
func (s *Server) apiSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.signedIn() {
		writeJSON(w, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	v, err := s.entitlements(ctx, r.URL.Query().Get("refresh") != "")
	switch {
	case errors.Is(err, creght.ErrPreview), errors.Is(err, creght.ErrNotLoggedIn):
		writeJSON(w, map[string]any{"available": false})
	case err != nil:
		writeJSON(w, map[string]any{"available": true, "error": err.Error()})
	default:
		writeJSON(w, map[string]any{"available": true, "entitlements": v})
	}
}
