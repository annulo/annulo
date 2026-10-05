package localsite

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 平台下发的渲染配置（GET <api host>/api/tr/system_info 的 render_config）：
// 系统 importMap（react、talizen、lucide-react……的 CDN 地址）和不许站点覆盖的 specifier。
// 不写死在 Shuttle 里：平台升级了 react / talizen，本地渲染跟着用同一份。
// 本地用 dev_import_map（开发版 React：React Refresh 要它，报错信息也全），平台没给时退回 import_map。
type renderConfig struct {
	ImportMap       map[string]string `json:"import_map"`
	DevImportMap    map[string]string `json:"dev_import_map"`
	IgnoreImportMap []string          `json:"ignore_import_map"`
	// CDN：用随安装包带的那份时换成的 CDN（默认 esm.sh）；空是平台给的，地址原样用
	CDN string `json:"-"`
}

// 不连 creght 时（离线项目、没登录）用随安装包带的那份渲染配置（docs/annulo-plan.md 第 4 步）：
// 平台 render_config 的快照，包从可配置的 CDN 加载（esm.talizen.com 换成 esm.sh 这类，同一套 URL 写法）。
// talizen 包本身是 MIT、发在 npm 上，从公共 CDN 加载照常能用；creght 项目两边都用平台那份，和云端同构。
// 快照随发版更新：make render-config 重新拉一次。
//
//go:embed default_render_config.json
var defaultRenderConfigJSON []byte

// platformCDN 是平台 importMap 里的 CDN；DefaultCDN 是不连 creght 时默认换成的。
const (
	platformCDN = "https://esm.talizen.com"
	DefaultCDN  = "https://esm.sh"
)

// cdnURL 把平台 CDN 的地址换到 cdn 上（cdn 为空不换）。
func cdnURL(u, cdn string) string {
	if cdn == "" || cdn == platformCDN {
		return u
	}
	if strings.HasPrefix(u, platformCDN+"/") {
		return strings.TrimRight(cdn, "/") + u[len(platformCDN):]
	}
	return u
}

// bundledConfig 是随安装包带的渲染配置，地址换到 cdn 上。
func bundledConfig(cdn string) *renderConfig {
	var c renderConfig
	if err := json.Unmarshal(defaultRenderConfigJSON, &c); err != nil {
		panic("default_render_config.json: " + err.Error())
	}
	for _, m := range []map[string]string{c.ImportMap, c.DevImportMap} {
		for k, v := range m {
			m[k] = cdnURL(v, cdn)
		}
	}
	c.CDN = cdn
	return &c
}

func (c *renderConfig) imports() map[string]string {
	if len(c.DevImportMap) > 0 {
		return c.DevImportMap
	}
	return c.ImportMap
}

// Shuttle 给页面补的包（平台的系统 importMap 里没有）：前端路由用 react-router（8.x 要求 react ≥ 19.2.7，平台现在是 19.2.4，先用 7.x）；
// 热更新用 react-refresh 的运行时（runtime.go）。
var shuttleImports = map[string]string{
	"react-router":          "https://esm.talizen.com/react-router@7.18.4?bundle&dev&external=react,react-dom",
	"react-refresh/runtime": "https://esm.talizen.com/react-refresh@0.18.0/runtime?dev",
}

// systemConfig 取平台的渲染配置：内存里缓存 10 分钟，磁盘上留一份给离线启动用。
// apiHost 为空（不连 creght）时直接用随安装包带的那份（cdn 上加载）；平台读不到、也没有缓存时同样退回它。
type systemConfig struct {
	apiHost string
	cdn     string // 用随安装包带的那份时的 CDN，空是 DefaultCDN
	file    string

	mu  sync.Mutex
	cfg *renderConfig
	at  time.Time
}

func (s *systemConfig) get(ctx context.Context) (*renderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apiHost == "" {
		if s.cfg == nil {
			s.cfg = bundledConfig(s.cdnOrDefault())
		}
		return s.cfg, nil
	}
	if s.cfg != nil && time.Since(s.at) < 10*time.Minute {
		return s.cfg, nil
	}
	cfg, err := fetchRenderConfig(ctx, s.apiHost)
	if err == nil {
		s.cfg, s.at = cfg, time.Now()
		if b, err := json.Marshal(cfg); err == nil {
			_ = os.MkdirAll(filepath.Dir(s.file), 0o755)
			_ = os.WriteFile(s.file, b, 0o644)
		}
		return cfg, nil
	}
	if s.cfg != nil {
		return s.cfg, nil // 平台暂时连不上：接着用上一份
	}
	if b, rerr := os.ReadFile(s.file); rerr == nil {
		var cached renderConfig
		if json.Unmarshal(b, &cached) == nil && len(cached.ImportMap) > 0 {
			s.cfg, s.at = &cached, time.Now().Add(-9*time.Minute) // 一分钟后再试平台
			return s.cfg, nil
		}
	}
	// 平台读不到、也没有缓存：用随安装包带的那份（地址还是平台的 CDN），一分钟后再试平台
	s.cfg, s.at = bundledConfig(s.cdn), time.Now().Add(-9*time.Minute)
	return s.cfg, nil
}

func (s *systemConfig) cdnOrDefault() string {
	if s.cdn != "" {
		return s.cdn
	}
	return DefaultCDN
}

func fetchRenderConfig(ctx context.Context, apiHost string) (*renderConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lastErr error
	for _, p := range []string{"/api/tr/system_info", "/api/r/system_info"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiHost, "/")+p, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var body struct {
			RenderConfig renderConfig `json:"render_config"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", p, resp.StatusCode)
			continue
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", p, err)
			continue
		}
		if len(body.RenderConfig.ImportMap) == 0 {
			lastErr = fmt.Errorf("%s: render_config.import_map is empty", p)
			continue
		}
		return &body.RenderConfig, nil
	}
	return nil, lastErr
}

// mergeImportMap：系统的 + Shuttle 补的 + 站点 talizen.config.ts 里的（站点的会补上 external=react,react-dom），
// 最后把系统保护的 specifier（react 这类）改回系统那份。和平台 engine.Load 的合并顺序一致。
func mergeImportMap(sys *renderConfig, site map[string]string) map[string]string {
	out := map[string]string{}
	if sys != nil {
		for k, v := range sys.imports() {
			out[k] = v
		}
	}
	for k, v := range shuttleImports {
		if _, ok := out[k]; !ok {
			if sys != nil {
				v = cdnURL(v, sys.CDN)
			}
			out[k] = v
		}
	}
	for k, v := range normalizeImportMapExternalDeps(site) {
		out[k] = v
	}
	if sys != nil {
		for _, k := range sys.IgnoreImportMap {
			if v, ok := sys.imports()[k]; ok {
				out[k] = v
			}
		}
	}
	return out
}

// 以下照抄平台渲染器（normalizeImportMapExternal 一族）：
// 站点自己配的 CDN 包要和页面共用同一份 react，URL 上补 external=react,react-dom。

var requiredImportMapExternalDeps = []string{"react", "react-dom"}

func normalizeImportMapExternalDeps(imports map[string]string) map[string]string {
	if len(imports) == 0 {
		return imports
	}
	out := make(map[string]string, len(imports))
	for specifier, importURL := range imports {
		out[specifier] = normalizeImportMapExternal(specifier, importURL, importMapExternalDeps(specifier, imports))
	}
	return out
}

func normalizeImportMapExternal(specifier string, importURL string, externalDeps []string) string {
	if isReactRuntimeSpecifier(specifier) || !isExternalImportURL(importURL) || len(externalDeps) == 0 {
		return importURL
	}
	beforeHash, hash := importURL, ""
	if idx := strings.Index(beforeHash, "#"); idx >= 0 {
		hash, beforeHash = beforeHash[idx:], beforeHash[:idx]
	}
	if isImportMapPrefixMapping(specifier, beforeHash) {
		if normalized, ok := normalizeESMShPrefixExternal(beforeHash, externalDeps); ok {
			return normalized + hash
		}
		return importURL
	}
	externalKeyIndex := strings.Index(beforeHash, "?external=")
	if externalKeyIndex < 0 {
		externalKeyIndex = strings.Index(beforeHash, "&external=")
	}
	if externalKeyIndex >= 0 {
		start := externalKeyIndex + 1 + len("external=")
		end := strings.Index(beforeHash[start:], "&")
		if end < 0 {
			end = len(beforeHash)
		} else {
			end = start + end
		}
		return beforeHash[:start] + mergeExternalParamValue(beforeHash[start:end], externalDeps) + beforeHash[end:] + hash
	}
	separator := "?"
	if strings.Contains(beforeHash, "?") {
		separator = "&"
	}
	return beforeHash + separator + "external=" + strings.Join(externalDeps, ",") + hash
}

// 前缀条目（如 "three/addons/"）额外 external 本包，让插件里的 import "three" 复用站点那份。
func importMapExternalDeps(specifier string, imports map[string]string) []string {
	deps := append([]string(nil), requiredImportMapExternalDeps...)
	if !strings.HasSuffix(specifier, "/") {
		return deps
	}
	base := packageName(specifier)
	if base == "" || base == specifier || isReactRuntimeSpecifier(base) {
		return deps
	}
	if _, ok := imports[base]; !ok {
		return deps
	}
	for _, d := range deps {
		if d == base {
			return deps
		}
	}
	return append(deps, base)
}

func isImportMapPrefixMapping(specifier, importURL string) bool {
	return strings.HasSuffix(specifier, "/") && strings.HasSuffix(importURL, "/")
}

func normalizeESMShPrefixExternal(importURL string, externalDeps []string) (string, bool) {
	u, err := url.Parse(importURL)
	if err != nil || u.RawQuery != "" {
		return "", false
	}
	if h := strings.ToLower(u.Hostname()); h != "esm.sh" && h != "esm.talizen.com" && !isConfiguredCDN(h) {
		return "", false
	}
	end := esmShPackagePathEnd(u.Path)
	if end <= 0 {
		return "", false
	}
	seg := u.Path[1:end]
	if !esmShPackageSegmentHasVersion(seg) {
		return "", false
	}
	if i := strings.Index(seg, "&external="); i >= 0 {
		start := i + len("&external=")
		e := strings.Index(seg[start:], "&")
		if e < 0 {
			e = len(seg)
		} else {
			e = start + e
		}
		seg = seg[:start] + mergeExternalParamValue(seg[start:e], externalDeps) + seg[e:]
	} else {
		seg += "&external=" + strings.Join(externalDeps, ",")
	}
	u.Path = "/" + seg + u.Path[end:]
	return u.String(), true
}

func esmShPackageSegmentHasVersion(seg string) bool {
	if i := strings.Index(seg, "&"); i >= 0 {
		seg = seg[:i]
	}
	if strings.HasPrefix(seg, "@") {
		i := strings.Index(seg, "/")
		return i >= 0 && strings.Contains(seg[i+1:], "@")
	}
	return strings.Contains(seg, "@")
}

func esmShPackagePathEnd(p string) int {
	if !strings.HasPrefix(p, "/") || p == "/" {
		return 0
	}
	if strings.HasPrefix(p, "/@") {
		scopeEnd := strings.Index(p[1:], "/")
		if scopeEnd < 0 {
			return 0
		}
		start := scopeEnd + 2
		e := strings.Index(p[start:], "/")
		if e < 0 {
			return len(p)
		}
		return start + e
	}
	e := strings.Index(p[1:], "/")
	if e < 0 {
		return len(p)
	}
	return e + 1
}

func isReactRuntimeSpecifier(s string) bool {
	return s == "react" || strings.HasPrefix(s, "react/") || s == "react-dom" || strings.HasPrefix(s, "react-dom/")
}

func isExternalImportURL(u string) bool {
	u = strings.ToLower(u)
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func mergeExternalParamValue(value string, required []string) string {
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range append(strings.Split(value, ","), required...) {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return strings.Join(out, ",")
}

// packageName：@scope/pkg/sub → @scope/pkg，pkg/sub → pkg。
func packageName(spec string) string {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// configuredCDNs 是设置里配的 CDN 的主机名（用 esm.sh 同一套 URL 写法）：站点自己配的包也按它补 external。
var configuredCDNs sync.Map

func isConfiguredCDN(host string) bool {
	_, ok := configuredCDNs.Load(host)
	return ok
}
