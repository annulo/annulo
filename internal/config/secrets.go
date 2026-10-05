package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/annulo/annulo/internal/i18n"
)

// 本机密钥：~/.shuttle/secrets.json（0600），比如 PERPLEXITY_API_KEY。只在本机用：
// 本机函数通过 ctx.secrets.get 读；MCP 配置里的 ${NAME}、模型的 api_key_env 通过 Lookup 取。
// **不设进 Shuttle 的进程环境变量**：助手的 bash 继承进程环境，设进去它就能 echo 出来；密钥不给 AI 助手读。
// 永远不上传、不进对话历史、不写进运营后台的代码和表。

var secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

type Secrets struct {
	path string
	mu   sync.Mutex
	m    map[string]string
}

// current 是这个进程加载的密钥：给 Lookup 用（MCP、模型配置不持有 *Secrets）
var current *Secrets

func (c *Config) LoadSecrets() *Secrets {
	s := &Secrets{path: filepath.Join(c.Dir, "secrets.json"), m: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.m)
	}
	current = s
	return s
}

// Lookup 按名字取密钥（设置里存的优先，没有再看终端 export 的环境变量），给 os.Expand 用：MCP 配置的 ${NAME}、模型的 api_key_env。
func Lookup(name string) string {
	if current != nil {
		v, _ := current.Get(name)
		return v
	}
	return os.Getenv(name)
}

// All 是设置里存的全部密钥（NAME=值），给用户自己配的 MCP 本地进程当环境变量；不给助手的命令行。
func All() []string {
	if current == nil {
		return nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	out := make([]string, 0, len(current.m))
	for k, v := range current.m {
		out = append(out, k+"="+v)
	}
	return out
}

// Get 取密钥：设置里存的优先，没有再看环境变量（终端里 export 的）。
func (s *Secrets) Get(name string) (string, bool) {
	s.mu.Lock()
	v, ok := s.m[name]
	s.mu.Unlock()
	if ok && v != "" {
		return v, true
	}
	v = os.Getenv(name)
	return v, v != ""
}

// SecretInfo 是给界面看的：名字、尾号、来源，不含值。
type SecretInfo struct {
	Name   string `json:"name"`
	Hint   string `json:"hint"`
	Source string `json:"source"` // shuttle：设置里存的；env：终端环境变量
}

func hint(v string) string {
	if len(v) >= 8 {
		return "…" + v[len(v)-4:]
	}
	return "…"
}

// List 是设置里存的密钥；names 里额外列出的（比如函数用到的）如果只在环境变量里有，也列出来。
func (s *Secrets) List(names ...string) []SecretInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SecretInfo{}
	seen := map[string]bool{}
	for k, v := range s.m {
		out = append(out, SecretInfo{Name: k, Hint: hint(v), Source: "shuttle"})
		seen[k] = true
	}
	for _, n := range names {
		if v := os.Getenv(n); v != "" && !seen[n] {
			out = append(out, SecretInfo{Name: n, Hint: hint(v), Source: "env"})
			seen[n] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Secrets) Set(name, value string) error {
	if !secretNameRe.MatchString(name) {
		return i18n.New("名字用大写字母、数字和下划线，比如 PERPLEXITY_API_KEY", "Use uppercase letters, digits and underscores, e.g. PERPLEXITY_API_KEY")
	}
	if value == "" {
		return i18n.New("值是空的", "The value is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = value
	return s.save()
}

func (s *Secrets) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[name]; !ok {
		return i18n.Errorf("没有这个密钥：%s", "No such key: %s", name)
	}
	delete(s.m, name)
	return s.save()
}

func (s *Secrets) save() error {
	b, _ := json.MarshalIndent(s.m, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
