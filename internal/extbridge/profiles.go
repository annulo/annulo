package extbridge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type chromeName struct{ name, email string }

// names 记住对上过的：插件的 id 跟着 Chrome profile 走，对上一次就不会变
var names sync.Map // 插件 id → chromeName

// chromeProfileNames 把插件连接对到 Chrome 里的 profile，拿到用户认得的名字和邮箱。
// 插件自己拿不到 profile 的名字：Annulo 在各 profile 目录的 Local Extension Settings/<插件 id>/ 里找插件存的 id
// （chrome.storage.local 是 leveldb，id 在 .log / .ldb 里能直接搜到；压缩了就搜不到，那就不显示名字），
// 名字和邮箱从用户目录的 Local State（profile.info_cache）读。
func chromeProfileNames(conns []*Conn) map[string]chromeName {
	out := map[string]chromeName{}
	var missing [][]byte
	for _, c := range conns {
		if v, ok := names.Load(c.ID); ok {
			out[c.ID] = v.(chromeName)
		} else {
			missing = append(missing, []byte(c.ID))
		}
	}
	if len(missing) == 0 {
		return out
	}
	for _, root := range userDataDirs() {
		b, err := os.ReadFile(filepath.Join(root, "Local State"))
		if err != nil {
			continue
		}
		var st struct {
			Profile struct {
				InfoCache map[string]struct {
					Name     string `json:"name"`
					UserName string `json:"user_name"`
				} `json:"info_cache"`
			} `json:"profile"`
		}
		if json.Unmarshal(b, &st) != nil {
			continue
		}
		for dir, info := range st.Profile.InfoCache {
			for _, ext := range ExtensionIDs {
				files, _ := filepath.Glob(filepath.Join(root, dir, "Local Extension Settings", ext, "*"))
				for _, f := range files {
					data, err := os.ReadFile(f)
					if err != nil {
						continue
					}
					for _, id := range missing {
						if bytes.Contains(data, id) {
							n := chromeName{name: info.Name, email: info.UserName}
							names.Store(string(id), n)
							out[string(id)] = n
						}
					}
				}
			}
		}
	}
	return out
}

// userDataDirs 是本机 Chromium 系浏览器的用户目录
func userDataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var list []string
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		for _, b := range []string{"Google/Chrome", "Google/Chrome Beta", "Chromium", "Microsoft Edge", "BraveSoftware/Brave-Browser"} {
			list = append(list, filepath.Join(base, filepath.FromSlash(b)))
		}
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		for _, b := range []string{`Google\Chrome\User Data`, `Microsoft\Edge\User Data`, `Chromium\User Data`, `BraveSoftware\Brave-Browser\User Data`} {
			list = append(list, filepath.Join(base, b))
		}
	default:
		base := filepath.Join(home, ".config")
		for _, b := range []string{"google-chrome", "chromium", "microsoft-edge", "BraveSoftware/Brave-Browser"} {
			list = append(list, filepath.Join(base, filepath.FromSlash(b)))
		}
	}
	return list
}
