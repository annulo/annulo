package localsite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// 不连 creght：用随安装包带的渲染配置，包从 CDN（默认 esm.sh）加载，talizen 照常能用（MIT，发在 npm 上）。
func TestBundledRenderConfig(t *testing.T) {
	sc := systemConfig{file: filepath.Join(t.TempDir(), "x.json")}
	cfg, err := sc.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	im := mergeImportMap(cfg, nil)
	for _, k := range []string{"react", "react-dom", "talizen", "talizen/", "lucide-react", "react-router", "react-refresh/runtime"} {
		if !strings.HasPrefix(im[k], DefaultCDN+"/") {
			t.Errorf("%s 要从 %s 加载：%q", k, DefaultCDN, im[k])
		}
	}
	if cdnURL(tailwindBrowserURL, cfg.CDN) != DefaultCDN+"/@tailwindcss/browser@4.2.2" {
		t.Fatal(cdnURL(tailwindBrowserURL, cfg.CDN))
	}

	// 自己配的 CDN
	sc = systemConfig{cdn: "https://cdn.example.com", file: filepath.Join(t.TempDir(), "x.json")}
	cfg, _ = sc.get(context.Background())
	if im := mergeImportMap(cfg, nil); !strings.HasPrefix(im["talizen"], "https://cdn.example.com/talizen@") {
		t.Fatal(im["talizen"])
	}
}

// 连着 creght 但平台读不到、也没有缓存：退回随安装包带的那份，地址还是平台的 CDN，页面照样能渲染。
func TestPlatformUnreachableFallsBack(t *testing.T) {
	sc := systemConfig{apiHost: "http://127.0.0.1:1", file: filepath.Join(t.TempDir(), "x.json")}
	cfg, err := sc.get(context.Background())
	if err != nil || cfg == nil {
		t.Fatalf("要退回随安装包带的：%v", err)
	}
	if im := mergeImportMap(cfg, nil); !strings.HasPrefix(im["react"], platformCDN+"/") {
		t.Fatal(im["react"])
	}
}
