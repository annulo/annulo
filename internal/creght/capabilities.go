package creght

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// 能力清单（weave docs/architecture/selfhost-export.md「system_info 能力清单」）：GET <host>/api/tr/system_info 的 capabilities。
// SaaS 和自己部署的 creght 都报；Annulo 只看 key 在不在，不看 deployment。没有的能力对应的功能不出现（模板、平台模型、中转…）。

type SystemInfo struct {
	Deployment   string                           `json:"deployment"`
	APIVersion   int                              `json:"api_version"`
	Capabilities map[string]struct{ Version int } `json:"capabilities"`
}

var capCache = struct {
	sync.Mutex
	m map[string]capEntry
}{m: map[string]capEntry{}}

type capEntry struct {
	info *SystemInfo
	at   time.Time
}

// FetchSystemInfo 读一个 creght 地址的 system_info（不用登录）。
func FetchSystemInfo(ctx context.Context, host string) (*SystemInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", normHost(host)+"/api/tr/system_info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("system_info: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data *SystemInfo `json:"data"`
		SystemInfo
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data != nil {
		return body.Data, nil
	}
	return &body.SystemInfo, nil
}

// systemInfo 是缓存的（成功的 10 分钟、失败的 1 分钟）；读不到返回 nil。
func systemInfo(host string) *SystemInfo {
	h := normHost(host)
	capCache.Lock()
	e, ok := capCache.m[h]
	capCache.Unlock()
	ttl := 10 * time.Minute
	if e.info == nil {
		ttl = time.Minute
	}
	if ok && time.Since(e.at) < ttl {
		return e.info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	info, err := FetchSystemInfo(ctx, h)
	if err != nil {
		info = nil
	}
	if info == nil && e.info != nil { // 一时读不到：用上次的
		info = e.info
	}
	capCache.Lock()
	capCache.m[h] = capEntry{info: info, at: time.Now()}
	capCache.Unlock()
	return info
}

// Has：这个 creght 有没有某个能力。读不到能力清单、或者清单是空的（老版本平台还没有这个字段），当作有（照以前的行为）。
func Has(host, capability string) bool {
	info := systemInfo(host)
	if info == nil || len(info.Capabilities) == 0 {
		return true
	}
	_, ok := info.Capabilities[capability]
	return ok
}
