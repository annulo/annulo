package extbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Register 把 native host 登记给本机的 Chromium 系浏览器（Chrome、Edge、Chromium、Brave）：写清单文件，Windows 上再写注册表。
// exe 是 annulo 程序的绝对路径（Chrome 直接拉起它，见 IsHostInvocation）；dataDir 是 Annulo 的数据目录（Windows 的清单放这里）。
// App 每次启动都写一遍：App 挪了位置、更新换了路径也跟着对。没装的浏览器跳过。
func Register(exe, dataDir string) error {
	origins := make([]string, 0, len(ExtensionIDs))
	for _, id := range ExtensionIDs {
		origins = append(origins, "chrome-extension://"+id+"/")
	}
	manifest, _ := json.MarshalIndent(map[string]any{
		"name":            HostName,
		"description":     "Annulo",
		"path":            exe,
		"type":            "stdio",
		"allowed_origins": origins,
	}, "", "  ")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	write := func(dir string) error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, HostName+".json"), append(manifest, '\n'), 0o644)
	}
	var firstErr error
	switch runtime.GOOS {
	case "darwin", "linux":
		var roots []string
		if runtime.GOOS == "darwin" {
			base := filepath.Join(home, "Library", "Application Support")
			for _, b := range []string{"Google/Chrome", "Google/Chrome Beta", "Google/Chrome Canary", "Chromium", "Microsoft Edge", "BraveSoftware/Brave-Browser"} {
				roots = append(roots, filepath.Join(base, filepath.FromSlash(b)))
			}
		} else {
			base := filepath.Join(home, ".config")
			for _, b := range []string{"google-chrome", "google-chrome-beta", "chromium", "microsoft-edge", "BraveSoftware/Brave-Browser"} {
				roots = append(roots, filepath.Join(base, filepath.FromSlash(b)))
			}
		}
		for _, root := range roots {
			if st, err := os.Stat(root); err != nil || !st.IsDir() {
				continue
			}
			if err := write(filepath.Join(root, "NativeMessagingHosts")); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	case "windows":
		dir := filepath.Join(dataDir, "browser-extension")
		if err := write(dir); err != nil {
			return err
		}
		file := filepath.Join(dir, HostName+".json")
		for _, key := range []string{`Google\Chrome`, `Microsoft\Edge`, `Chromium`, `BraveSoftware\Brave-Browser`} {
			if err := setRegistry(`Software\`+key+`\NativeMessagingHosts\`+HostName, file); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("registry %s: %w", key, err)
			}
		}
	}
	return firstErr
}
