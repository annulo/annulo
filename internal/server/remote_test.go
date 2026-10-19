package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/mcphub"
)

func remoteTestServer(t *testing.T) (*Server, http.Handler) {
	root := t.TempDir()
	creght.WriteOffline(filepath.Join(root, "backends", "local-a"), creght.OfflineProject{ProjectID: "local-a", Name: "a"})
	cfg := &config.Config{Dir: root, Port: 7799, Backend: "local-a", Offline: true, RemoteTunnel: true}
	ag := agent.New(cfg)
	s := newTestServer(cfg, nil, ag)
	s.mcp = mcphub.New(root)
	s.web = fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}}
	s.relay = s.newRelay()   // 不连
	s.tunnel = s.newTunnel() // 不连，只为接口路由
	t.Cleanup(func() {
		for _, v := range s.views() {
			v.Detach()
		}
		localdb.Close(s.offlineDBPath("local-a"))
	})
	return s, s.remoteHandler(s.Handler())
}

// 平台转来的请求：Host 是远程域名，带项目、账号、设备
func remoteReqFor(method, path, device string) *http.Request {
	r := httptest.NewRequest(method, "https://k3x9.remote.creght.cn"+path, nil)
	r.Header.Set(ProjectHeader, "local-a")
	r.Header.Set(remoteUserHeader, "u1")
	if device != "" {
		r.Header.Set(remoteDeviceHeader, device)
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	if strings.HasPrefix(path, "/_shuttle/api/") {
		r.Header.Set("X-Annulo", "1")
	}
	return r
}

func TestRemoteDeviceApproval(t *testing.T) {
	s, h := remoteTestServer(t)
	do := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do(remoteReqFor("GET", "/_shuttle/api/remote/devices", "")); w.Code != http.StatusForbidden {
		t.Fatalf("没带设备要拒：%d", w.Code)
	}

	// 新设备：页面是「等电脑上确认」，接口回 device_pending
	r := remoteReqFor("GET", "/_shuttle/", "dev1")
	r.Header.Set("Sec-Fetch-Dest", "document")
	if w := do(r); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "iPhone · Safari") {
		t.Fatalf("新设备要等确认：%d %s", w.Code, w.Body.String())
	}
	if w := do(remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev1")); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "device_pending") {
		t.Fatalf("没确认的设备调接口要回 device_pending：%d %s", w.Code, w.Body.String())
	}
	if d := s.devices().all(); len(d) != 1 || d[0].Status != devicePending || d[0].User != "u1" {
		t.Fatalf("要记下等确认的设备：%+v", d)
	}

	// 远程不能自己确认
	if w := do(remoteReqFor("POST", "/_shuttle/api/remote/devices/dev1/approve", "dev1")); w.Code != http.StatusForbidden {
		t.Fatalf("没确认的设备不能确认自己：%d", w.Code)
	}
	if !s.devices().approve("dev1") {
		t.Fatal("approve")
	}

	// 确认后：外壳不跳到 localhost，接口能用
	r = remoteReqFor("GET", "/_shuttle/", "dev1")
	r.Header.Set("Sec-Fetch-Dest", "document")
	if w := do(r); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "shell") {
		t.Fatalf("远程打开外壳不能跳到本机地址：%d %v", w.Code, w.Header())
	}
	w := do(remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev1"))
	var out struct {
		List []map[string]any `json:"list"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.List) != 1 || out.List[0]["active"] != true {
		t.Fatalf("确认过的设备能调接口、显示在用：%d %s", w.Code, w.Body.String())
	}

	// 确认过的设备也碰不了：本机端口、确认别的设备、开关隧道、MCP
	for _, c := range []struct{ method, path string }{
		{"GET", "/_shuttle/port/5432/"},
		{"GET", "/_annulo/port/8000/x"},
		{"POST", "/_shuttle/api/remote/devices/dev2/approve"},
		{"DELETE", "/_shuttle/api/remote/devices/dev1"},
		{"PUT", "/_shuttle/api/settings/remote/tunnel"},
		{"PUT", "/_shuttle/api/settings/remote"},
		{"POST", "/_shuttle/mcp"},
		{"POST", "/_shuttle/api/local/open"},
		{"POST", "/_shuttle/api/settings/browser/chrome"},
	} {
		if w := do(remoteReqFor(c.method, c.path, "dev1")); w.Code != http.StatusForbidden {
			t.Errorf("%s %s 远程要拒：%d", c.method, c.path, w.Code)
		}
	}

	// 跨站照样挡
	r = remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev1")
	r.Header.Set("Origin", "https://evil.com")
	if w := do(r); w.Code != http.StatusForbidden {
		t.Fatalf("Origin 不一致要拒：%d", w.Code)
	}

	// 撤销后又要等确认
	s.devices().remove("dev1")
	if w := do(remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev1")); w.Code != http.StatusForbidden {
		t.Fatalf("撤销后要重新确认：%d", w.Code)
	}

	// 电脑上关了远程访问：一律拒
	s.devices().approve("dev1")
	s.cfg.RemoteTunnel = false
	if w := do(remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev1")); w.Code != http.StatusForbidden {
		t.Fatalf("关了远程访问要拒：%d", w.Code)
	}
}

// 本机请求带上平台的头也不算远程：远程只认隧道打的标记
func TestRemoteHeadersIgnoredLocally(t *testing.T) {
	s, _ := remoteTestServer(t)
	r := httptest.NewRequest("GET", "https://k3x9.remote.creght.cn/_shuttle/api/remote/devices", nil)
	r.Header.Set(remoteDeviceHeader, "dev1")
	r.Header.Set("X-Annulo", "1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("本机端口上远程域名的 Host 要拒：%d", w.Code)
	}
}

func TestDeviceName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0 Mobile/15E148 Safari/604.1": "iPhone · Chrome",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36":                    "Android · Chrome",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0":      "iPhone · 微信",
	} {
		if got := deviceName(ua); got != want {
			t.Errorf("%s → %q，要 %q", ua, got, want)
		}
	}
}

// 平台给了设备名就用它，不再从 User-Agent 猜
func TestRemoteDeviceNameHeader(t *testing.T) {
	s, h := remoteTestServer(t)
	r := remoteReqFor("GET", "/_shuttle/api/remote/devices", "dev9")
	r.Header.Set(remoteDeviceNameHeader, "Pixel · Chrome")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if d := s.devices().all(); len(d) != 1 || d[0].Name != "Pixel · Chrome" {
		t.Fatalf("%+v", d)
	}
}

// 远程打开根路径、后台的某一页：直接出外壳，地址栏不带 /_shuttle/；本机照旧跳到 /_shuttle/
func TestRemoteShellAtRoot(t *testing.T) {
	s, h := remoteTestServer(t)
	s.devices().seen(&remoteReq{Device: "dev1"})
	s.devices().approve("dev1")
	for _, p := range []string{"/", "/content?id=3"} {
		r := remoteReqFor("GET", p, "dev1")
		r.Header.Set("Sec-Fetch-Dest", "document")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "shell") {
			t.Fatalf("%s 远程要直接出外壳：%d %v", p, w.Code, w.Header())
		}
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7799/content", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/_shuttle/") {
		t.Fatalf("本机照旧跳到 /_shuttle/：%d %v", w.Code, w.Header())
	}
}
