package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/version"
)

// 服务端部署的「当前版本 / 有新版本」（AGENTS.md「服务端部署：配置都在页面上」）：读官网的 update.json（和桌面 App 内更新同一份），
// 和自己的版本比。镜像的版本号构建时从 changelog.yaml 取（Dockerfile）：最近一次正式发版的版本，最上面还有没发的 next 时带 +next。
// 升级还要在服务器上跑命令（拉代码、构建、compose up），页面上给出命令；一键升级以后再做。

const defaultUpdateFeed = "https://annulo.dev/update.json"

type updateInfo struct {
	Version     string            `json:"version"`
	PublishedAt string            `json:"published_at"`
	Notes       string            `json:"notes"`
	NotesI18n   map[string]string `json:"notes_i18n"`
}

var updateFetching atomic.Bool // 顶栏触发的后台取数同时只跑一个

var updateCache struct {
	sync.Mutex
	info *updateInfo
	err  string
	at   time.Time
}

func updateFeed() string {
	if v := brand.Env("UPDATE_URL"); v != "" {
		return v
	}
	return defaultUpdateFeed
}

// latestRelease 是官网上的最新版本（缓存 1 小时，失败 5 分钟后再试）；refresh 为 true 时现取。
func latestRelease(ctx context.Context, refresh bool) (*updateInfo, string) {
	updateCache.Lock()
	info, errMsg, at := updateCache.info, updateCache.err, updateCache.at
	updateCache.Unlock()
	ttl := time.Hour
	if errMsg != "" {
		ttl = 5 * time.Minute
	}
	if !refresh && !at.IsZero() && time.Since(at) < ttl {
		return info, errMsg
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, updateFeed(), nil)
	var got updateInfo
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = i18n.Errorf("HTTP %d", "HTTP %d", resp.StatusCode)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&got)
		}
	}
	updateCache.Lock()
	defer updateCache.Unlock()
	updateCache.at = time.Now()
	if err != nil || got.Version == "" {
		if err == nil {
			err = i18n.New("官网的版本信息是空的", "The website's version info is empty")
		}
		updateCache.err = err.Error()
		return updateCache.info, updateCache.err // 读不到：用上次的
	}
	updateCache.info, updateCache.err = &got, ""
	return &got, ""
}

// versionParts：2026.10.15-2 → [2026 10 15 2]；+next（这个版本之后还没发的改动）去掉。不是这种格式返回 nil（dev、提交号）。
func versionParts(v string) []int {
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "+")
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' }) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	if len(out) < 3 {
		return nil
	}
	return out
}

// newerVersion：latest 比 current 新。比不了（current 是 dev 之类）返回 false。
func newerVersion(latest, current string) bool {
	a, b := versionParts(latest), versionParts(current)
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// upgradeSteps 是服务器上升级要跑的命令（在 clone 下来的 annulo 目录里）。
const upgradeSteps = "cd annulo && git pull\ndocker build -t annulo/annulo:latest .\ncd deploy && docker compose up -d"

// apiServerVersion：GET settings/version[?refresh=1]，服务端部署的当前版本、官网最新版本和更新说明、升级命令。
func (s *Server) apiServerVersion(w http.ResponseWriter, r *http.Request) {
	info, errMsg := latestRelease(r.Context(), r.URL.Query().Get("refresh") == "1")
	out := map[string]any{"current": version.Version, "newer": false, "error": errMsg, "upgrade": upgradeSteps, "comparable": versionParts(version.Version) != nil}
	if info != nil {
		notes := info.NotesI18n[i18n.Locale()]
		if notes == "" {
			notes = info.Notes
		}
		out["latest"], out["published_at"], out["notes"] = info.Version, info.PublishedAt, notes
		out["newer"] = newerVersion(info.Version, version.Version)
	}
	writeJSON(w, out)
}

// updateNotice：给管理员顶栏的「有新版本」（只用缓存，不在 status 里等网络；缓存过期时后台去取）。
func (s *Server) updateNotice(r *http.Request) map[string]any {
	if !s.cfg.Server {
		return nil
	}
	if u := userOf(r.Context()); u == nil || u.Role != "admin" {
		return nil
	}
	updateCache.Lock()
	info, at := updateCache.info, updateCache.at
	updateCache.Unlock()
	if (at.IsZero() || time.Since(at) > time.Hour) && updateFetching.CompareAndSwap(false, true) {
		go func() {
			defer updateFetching.Store(false)
			latestRelease(context.Background(), false)
		}()
	}
	if info == nil || !newerVersion(info.Version, version.Version) {
		return nil
	}
	return map[string]any{"current": version.Version, "latest": info.Version}
}
