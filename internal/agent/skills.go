package agent

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/sky-valley/pi/coding"
)

// skill 由 Shuttle 管理，只有这里列出来的才进 agent 的上下文：
//
//	内置   ~/.shuttle/builtin-skills/<name>   随 Shuttle 发布（embed），每次启动覆盖，不能停用
//	已安装 ~/.shuttle/skills/<name>           用户或 agent 安装的，可以停用、删除
//
// 停用状态存在 ~/.shuttle/skills.json。用户其他 agent 的 skill 目录（~/.agents/skills、
// ~/.claude/skills）只作为「可导入」来源展示，不自动加载。

type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // builtin / workspace（运营后台项目 skills/ 下的）/ installed / local（本机可导入）
	Path        string `json:"path"`   // SKILL.md
	Enabled     bool   `json:"enabled"`
	Origin      string `json:"origin,omitempty"` // 从哪装的
	Linked      bool   `json:"linked,omitempty"` // 软链到本机其他 agent 的 skill，跟着它更新
}

type skillState struct {
	Disabled []string `json:"disabled"`
}

func (a *Agent) builtinDir() string { return filepath.Join(a.cfg.Dir, "builtin-skills") }

// workspaceSkillsDir 是运营后台项目里的 skill：业务流程（体检、内容、GEO…）跟着运营后台走，
// 模板复制时一起带走，agent 改后台模块时顺手改它（调用方持有 a.mu 或只读场景）。
func (a *Agent) workspaceSkillsDir() string {
	if a.cwd == "" {
		return ""
	}
	return filepath.Join(a.cwd, "skills")
}
func (a *Agent) installedDir() string { return filepath.Join(a.cfg.Dir, "skills") }
func (a *Agent) skillStateFile() string {
	return filepath.Join(a.cfg.Dir, "skills.json")
}

func (a *Agent) readSkillState() skillState {
	var st skillState
	if b, err := os.ReadFile(a.skillStateFile()); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func (a *Agent) writeSkillState(st skillState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(a.skillStateFile(), b, 0o600)
}

// localSkillRoots 是本机其他 agent 的 skill 目录，只用来「导入」。
func localSkillRoots() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".claude", "skills")}
}

// InstallSkills 启动时调用：写出内置 skill；第一次运行时把本机的 creght skill 软链进来
// （creght skill 跟着平台更新，软链能拿到最新版）。
func (a *Agent) InstallSkills() error {
	os.RemoveAll(a.builtinDir())
	err := fs.WalkDir(builtinSkills, "skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := builtinSkills.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(a.builtinDir(), strings.TrimPrefix(p, "skills/"))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return err
	}
	// 老版本把 skill 放在工作目录的 .pi/skills 下，清掉
	os.RemoveAll(filepath.Join(a.cwd, ".pi"))

	if err := os.MkdirAll(a.installedDir(), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(a.installedDir(), "creght")); os.IsNotExist(err) && !a.firstRunDone() {
		for _, root := range localSkillRoots() {
			src := filepath.Join(root, "creght")
			if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err == nil {
				os.Symlink(src, filepath.Join(a.installedDir(), "creght"))
				break
			}
		}
	}
	a.markFirstRun()
	return nil
}

func (a *Agent) firstRunDone() bool {
	_, err := os.Stat(a.skillStateFile())
	return err == nil
}

func (a *Agent) markFirstRun() {
	if !a.firstRunDone() {
		a.writeSkillState(skillState{Disabled: []string{}})
	}
}

func loadDir(dir, source string) []SkillInfo {
	skills, _ := coding.LoadSkillsFromDir(dir)
	out := make([]SkillInfo, 0, len(skills))
	for _, s := range skills {
		info := SkillInfo{Name: s.Name, Description: s.Description, Source: source, Path: s.FilePath, Enabled: true}
		if source == "installed" {
			top := filepath.Join(dir, s.Name)
			if fi, err := os.Lstat(top); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				info.Linked = true
				info.Origin, _ = os.Readlink(top)
			} else {
				for _, f := range []string{".annulo-origin", ".shuttle-origin"} { // 新旧名字都认（internal/brand）
					if b, err := os.ReadFile(filepath.Join(s.BaseDir, f)); err == nil {
						info.Origin = strings.TrimSpace(string(b))
						break
					}
				}
			}
		}
		out = append(out, info)
	}
	return out
}

// ListSkills 返回内置、已安装和本机可导入的 skill。
func (a *Agent) ListSkills() (active, local []SkillInfo) {
	st := a.readSkillState()
	off := map[string]bool{}
	for _, n := range st.Disabled {
		off[n] = true
	}
	seen := map[string]bool{}
	for _, s := range loadDir(a.builtinDir(), "builtin") {
		seen[s.Name] = true
		active = append(active, s)
	}
	a.mu.Lock()
	wsDir := a.workspaceSkillsDir()
	a.mu.Unlock()
	if wsDir != "" {
		for _, s := range loadDir(wsDir, "workspace") {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			s.Enabled = !off[s.Name]
			active = append(active, s)
		}
	}
	for _, s := range loadDir(a.installedDir(), "installed") {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		s.Enabled = !off[s.Name]
		active = append(active, s)
	}
	for _, root := range localSkillRoots() {
		for _, s := range loadDir(root, "local") {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			s.Enabled = false
			local = append(local, s)
		}
	}
	sort.SliceStable(local, func(i, j int) bool { return local[i].Name < local[j].Name })
	return active, local
}

// sessionSkills 是给 pi 会话的 skill：内置 + 项目的 + 已启用的已安装（调用方持有 a.mu）。
func (a *Agent) sessionSkills() *[]coding.Skill {
	st := a.readSkillState()
	off := map[string]bool{}
	for _, n := range st.Disabled {
		off[n] = true
	}
	var out []coding.Skill
	seen := map[string]bool{}
	b, _ := coding.LoadSkillsFromDir(a.builtinDir())
	for _, s := range b {
		seen[s.Name] = true
		out = append(out, s)
	}
	if dir := a.workspaceSkillsDir(); dir != "" {
		ws, _ := coding.LoadSkillsFromDir(dir)
		for _, s := range ws {
			if !seen[s.Name] && !off[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
	}
	inst, _ := coding.LoadSkillsFromDir(a.installedDir())
	for _, s := range inst {
		if !seen[s.Name] && !off[s.Name] {
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	if out == nil {
		out = []coding.Skill{}
	}
	return &out
}

// skillsChanged：skill 集合是建会话时定的，空闲时丢掉当前会话，下一条消息带上新的（上下文从会话文件恢复）。
func (a *Agent) skillsChanged() {
	a.mu.Lock()
	a.dropSessions()
	a.mu.Unlock()
}

func (a *Agent) SetSkillEnabled(name string, on bool) error {
	st := a.readSkillState()
	var next []string
	for _, n := range st.Disabled {
		if n != name {
			next = append(next, n)
		}
	}
	if !on {
		next = append(next, name)
	}
	st.Disabled = next
	if st.Disabled == nil {
		st.Disabled = []string{}
	}
	if err := a.writeSkillState(st); err != nil {
		return err
	}
	a.skillsChanged()
	return nil
}

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (a *Agent) RemoveSkill(name string) error {
	if !skillNameRe.MatchString(name) {
		return i18n.Errorf("skill 名 %q 不合法", "Invalid skill name %q", name)
	}
	p := filepath.Join(a.installedDir(), name)
	if _, err := os.Lstat(p); err != nil {
		return i18n.Errorf("没有安装 %s", "%s isn't installed", name)
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	a.SetSkillEnabled(name, true) // 清掉停用记录
	return nil
}

// InstallSkill 从 source 安装 skill，返回装上的 skill。source 可以是：
//   - 本机目录（本身含 SKILL.md，或下面有若干个 skill）
//   - git 仓库地址；GitHub 的 …/tree/<分支>/<子目录> 链接只装那个子目录
//   - 直接指向 SKILL.md 的 http(s) 链接（只装这一个文件）
//
// link=true 且 source 是本机目录时用软链，跟着源目录更新（导入本机其他 agent 的 skill 时用）。
func (a *Agent) InstallSkill(ctx context.Context, source string, link bool) ([]SkillInfo, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, i18n.New("要填 skill 的来源：GitHub 链接、git 地址、SKILL.md 链接或本机路径", "Enter the skill source: a GitHub link, git URL, SKILL.md link or local path")
	}
	tmp, err := os.MkdirTemp("", "shuttle-skill-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	root := ""
	origin := source
	switch {
	case strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~"):
		if strings.HasPrefix(source, "~") {
			home, _ := os.UserHomeDir()
			source = filepath.Join(home, strings.TrimPrefix(source, "~"))
		}
		root = source
	case strings.HasSuffix(strings.ToLower(strings.SplitN(source, "?", 2)[0]), ".md"):
		if err := fetchSkillFile(ctx, githubRaw(source), filepath.Join(tmp, "skill", "SKILL.md")); err != nil {
			return nil, err
		}
		root = filepath.Join(tmp, "skill")
		link = false
	default:
		repo, branch, sub := parseGitSource(source)
		args := []string{"clone", "--depth", "1"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, repo, filepath.Join(tmp, "repo"))
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, i18n.Errorf("git clone 失败：%s", "git clone failed: %s", strings.TrimSpace(string(out)))
		}
		root = filepath.Join(tmp, "repo", sub)
		link = false
	}

	found, _ := coding.LoadSkillsFromDir(root)
	if len(found) == 0 {
		return nil, i18n.Errorf("%s 下没有找到 skill（需要含 SKILL.md、带 name 和 description 的目录）", "No skill found under %s (needs a folder with a SKILL.md that has name and description)", origin)
	}
	if err := os.MkdirAll(a.installedDir(), 0o755); err != nil {
		return nil, err
	}
	var installed []SkillInfo
	for _, s := range found {
		if !skillNameRe.MatchString(s.Name) {
			continue
		}
		dst := filepath.Join(a.installedDir(), s.Name)
		os.RemoveAll(dst)
		if link {
			if err := os.Symlink(s.BaseDir, dst); err != nil {
				return installed, err
			}
		} else {
			if err := copyDir(s.BaseDir, dst); err != nil {
				return installed, err
			}
			os.WriteFile(filepath.Join(dst, ".shuttle-origin"), []byte(origin+"\n"), 0o644)
		}
		installed = append(installed, SkillInfo{Name: s.Name, Description: s.Description, Source: "installed", Path: filepath.Join(dst, filepath.Base(s.FilePath)), Enabled: true, Origin: origin, Linked: link})
		a.SetSkillEnabled(s.Name, true)
	}
	if len(installed) == 0 {
		return nil, i18n.New("找到的 skill 名称不合法（只能用小写字母、数字和 -）", "The skill's name is invalid (use lowercase letters, digits and - only)")
	}
	a.skillsChanged()
	return installed, nil
}

// parseGitSource 解析 GitHub 的 tree 链接：https://github.com/o/r/tree/main/skills/x → 仓库、分支、子目录。
func parseGitSource(s string) (repo, branch, sub string) {
	u, err := url.Parse(s)
	if err != nil || u.Host != "github.com" {
		return s, "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return s, "", ""
	}
	repo = "https://github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git") + ".git"
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		branch = parts[3]
		sub = strings.Join(parts[4:], "/")
	}
	return repo, branch, sub
}

// githubRaw 把 GitHub 的 blob 链接换成 raw 链接。
func githubRaw(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host != "github.com" {
		return s
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 5 && parts[2] == "blob" {
		return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + strings.Join(parts[3:], "/")
	}
	return s
}

func fetchSkillFile(ctx context.Context, src, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return i18n.Errorf("下载 %s 失败：HTTP %d", "Failed to download %s: HTTP %d", src, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}
