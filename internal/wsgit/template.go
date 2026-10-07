package wsgit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 模板升级：项目是从模板（creght 上开了公开复制的项目）复制出来的，模板之后会发新版本（creght version）。
// 项目的 git 里有一条 template 分支，每个提交是模板的一个版本（快照），main 从它分出去、一路合并它。
// 升级 = 把模板新版本的快照提交到 template 分支，再在 main 上 git merge：
// 用户和助手改过的地方保留，模板改的地方合进来，两边改了同一处才是冲突（交给助手解决）。
// creght 模板本身不是 git；快照用平台接口按版本拉（public.go），只有模板发了版本的改动才会被拉到。git 模板见 gitsource.go。

// Template 是模板站点和它的快照放在本机的目录。
type Template struct {
	Site  string // <project_id>/<site_id>
	Cache string // 模板某一版的快照目录，换版本时原地更新
	// API 是当前 Shuttle 的能力版本（version.API）：模板版本的 shuttle.json 要求更高时不合并
	API int
	// Host 是模板所在的 creght 集群（平台接口的地址，public.go）
	Host string
}

// 模板 / 项目根目录用 shuttle.json（或新名字 annulo.json，brand.ProjectFiles）声明要求的能力版本：{"min_shuttle_api": 3}。

// MinAPI 读 min_annulo_api 或 min_shuttle_api（取大的）；没有字段就是 0（不要求）。
func MinAPI(b []byte) int {
	var v struct {
		MinShuttleAPI int `json:"min_shuttle_api"`
		MinAnnuloAPI  int `json:"min_annulo_api"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	return max(v.MinShuttleAPI, v.MinAnnuloAPI)
}

// dirMinAPI 是目录里 annulo.json / shuttle.json 要求的能力版本：两个都有取大的。
func dirMinAPI(dir string) int {
	n := 0
	for _, f := range brand.ProjectFiles {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			n = max(n, MinAPI(b))
		}
	}
	return n
}

// ProjectMinAPI 是项目目录里声明要求的能力版本。
func ProjectMinAPI(dir string) int {
	return dirMinAPI(dir)
}

var cacheMu sync.Mutex // 模板快照目录同一时间只拉一个版本

// VersionMinAPI 是模板第 n 版要求的能力版本：把那一版拉到 t.Cache（已经是那一版就不拉），读它的 annulo.json / shuttle.json。
func VersionMinAPI(ctx context.Context, t Template, n int) (int, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedVersion(t.Cache) != n {
		if err := pullVersion(ctx, t, n); err != nil {
			return 0, err
		}
	}
	return dirMinAPI(t.Cache), nil
}

// cachedVersion 是快照目录现在是模板的第几版（creght pull --version_no 记在 .creght/state.json 的 snapshot 里）。
// VersionDir 把模板第 n 版拉到 t.Cache（已经是那一版就不拉），返回这个目录：合并之前先看新版本里写了什么（比如要哪些插件）。
func VersionDir(ctx context.Context, t Template, n int) (string, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedVersion(t.Cache) != n {
		if err := pullVersion(ctx, t, n); err != nil {
			return "", err
		}
	}
	return t.Cache, nil
}

func cachedVersion(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, ".creght", "state.json"))
	if err != nil {
		return 0
	}
	var st struct {
		Snapshot struct {
			VersionNo int `json:"version_no"`
		} `json:"snapshot"`
	}
	json.Unmarshal(b, &st)
	return st.Snapshot.VersionNo
}

func pullVersion(ctx context.Context, t Template, n int) error {
	if err := os.MkdirAll(t.Cache, 0o755); err != nil {
		return err
	}
	if IsGitSite(t.Site) {
		files, err := filesGit(ctx, t, n)
		if err != nil {
			return err
		}
		return WriteSnapshot(t.Cache, t.Site, n, files)
	}
	return pullVersionPublic(ctx, t, n) // 平台接口，连着 creght 时带 token（付费模板按账号判断）
}

type Version struct {
	No        int    `json:"no"`
	Label     string `json:"label,omitempty"` // git 模板的 tag（v1.2.0）；creght 模板没有，界面显示「第 No 版」
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

// Versions 是模板的版本，新的在前。
func Versions(ctx context.Context, t Template) ([]Version, error) {
	if IsGitSite(t.Site) {
		return versionsGit(ctx, t)
	}
	return versionsPublic(ctx, t) // 平台接口，连着 creght 时带 token
}

const templateBranch = "refs/heads/template"

var (
	versionTrailer = regexp.MustCompile(`(?m)^模板版本: (\d+)$`)
	// siteTrailer 记这一版是哪个模板的（<project_id>/<site_id>）：换过模板的项目靠它认现在跟着哪个模板
	siteTrailer = regexp.MustCompile(`(?m)^模板站点: (\S+)$`)
)

// TemplateSite 是 template 分支上最近记的模板站点；没记过（老项目、没换过模板）返回 ""，这时按 creght 上的来源项目认。
func TemplateSite(dir string) string {
	msg, err := git(dir, "log", "-1", "--format=%B", templateBranch, "--")
	if err != nil {
		return ""
	}
	if m := siteTrailer.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

// Base 是项目当前基于模板的哪个版本（template 分支最新提交记的版本号）；还没记过返回 0。
func Base(dir string) int {
	msg, err := git(dir, "log", "-1", "--format=%B", templateBranch, "--")
	if err != nil {
		return 0
	}
	if m := versionTrailer.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// UpgradeResult 是一次升级的结果。
type UpgradeResult struct {
	Status    string   `json:"status"` // up_to_date | merged | conflict
	From      int      `json:"from"`
	To        int      `json:"to"`
	Files     []string `json:"files,omitempty"`     // 合并改到的文件
	Conflicts []string `json:"conflicts,omitempty"` // 有冲突的文件（合并停在进行中，等助手解决）
}

// Upgrade 把项目升级到模板的 to 版本（0 表示最新）。调用方保证这时没有别人在改工作目录（助手没在跑）。
// 有冲突时合并停在进行中：文件里是冲突标记，解决后提交（每轮自动提交或 annulo push）就完成合并；
// 冲突没解决前 Commit / Push 都会拒绝，不会把冲突标记提交或推上去。
func Upgrade(ctx context.Context, dir string, t Template, to int) (*UpgradeResult, error) {
	if err := Ensure(dir); err != nil {
		return nil, err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		return nil, i18n.New("上一次模板升级还有冲突没解决完：让助手解决冲突后再升级", "The last template upgrade still has unresolved conflicts: have the assistant resolve them, then upgrade")
	}
	vs, err := Versions(ctx, t)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, i18n.New("模板还没有发过版本", "The template has no published versions yet")
	}
	if to == 0 {
		to = vs[0].No
	}
	var target *Version
	for i := range vs {
		if vs[i].No == to {
			target = &vs[i]
		}
	}
	if target == nil {
		return nil, i18n.Errorf("模板没有版本 %d", "The template has no version %d", to)
	}
	if t.API > 0 {
		need, err := VersionMinAPI(ctx, t, to)
		if err != nil {
			return nil, err
		}
		if need > t.API {
			return nil, i18n.Errorf("模板 v%s 需要能力版本 %d 的 Annulo，这台电脑上的是 %d：先更新 Annulo 再升级", "Template v%s needs Annulo capability version %d; this computer has %d. Update Annulo first, then upgrade", vlabel(t.Site, to), need, t.API)
		}
	}
	if _, err := commit(dir, "升级模板前的改动"); err != nil {
		return nil, err
	}

	base := Base(dir)
	if base == 0 {
		if base, err = bootstrap(ctx, dir, t, vs); err != nil {
			return nil, err
		}
	}
	if base, err = advance(ctx, dir, t, vs, base, to); err != nil {
		return nil, err
	}
	res := &UpgradeResult{From: base, To: to}
	if base >= to {
		res.Status = "up_to_date"
		return res, nil
	}

	tree, err := snapshotTree(ctx, dir, t, to)
	if err != nil {
		return nil, err
	}
	parent, _ := git(dir, "rev-parse", templateBranch)
	if _, err := commitTemplate(dir, tree, strings.TrimSpace(parent), *target, t.Site); err != nil {
		return nil, err
	}
	before, _ := git(dir, "rev-parse", "HEAD")
	if _, err := git(dir, "merge", "--no-ff", "-m", fmt.Sprintf("升级模板：v%s → v%s", vlabel(t.Site, base), vlabel(t.Site, to)), templateBranch); err != nil {
		out, _ := git(dir, "diff", "--name-only", "-z", "--diff-filter=U")
		res.Conflicts = splitZ(out)
		if len(res.Conflicts) == 0 {
			git(dir, "merge", "--abort")
			return nil, err
		}
		res.Status = "conflict"
		return res, nil
	}
	out, _ := git(dir, "diff", "--name-only", "-z", strings.TrimSpace(before), "HEAD")
	res.Files = splitZ(out)
	res.Status = "merged"
	return res, nil
}

// Switch 把项目换成另一个模板（比如通用运营后台 → 外贸助手），合到新模板的 to 版本（0 表示最新）。
// 做法和升级一样是三方合并：新模板的快照接在 template 分支上旧模板那一版后面，再在 main 上合并——
// 项目自己改过的地方保留，两个模板之间的差别（行业文件、删掉的功能）合进来，两边改了同一处才冲突。
// 新模板的快照提交里记下模板站点（TemplateSite），之后按新模板认、跟着它升级。调用方保证没有别人在改工作目录。
func Switch(ctx context.Context, dir string, from, to Template, toVer int) (*UpgradeResult, error) {
	if from.Site == to.Site {
		return nil, i18n.New("已经是这个模板了", "The project already uses this template")
	}
	if err := Ensure(dir); err != nil {
		return nil, err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		return nil, i18n.New("上一次模板合并还有冲突没解决完：让助手解决冲突后再换模板", "The last template merge still has unresolved conflicts: have the assistant resolve them first")
	}
	vs, err := Versions(ctx, to)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, i18n.New("新模板还没有发过版本", "The new template has no published versions yet")
	}
	if toVer == 0 {
		toVer = vs[0].No
	}
	var target *Version
	for i := range vs {
		if vs[i].No == toVer {
			target = &vs[i]
		}
	}
	if target == nil {
		return nil, i18n.Errorf("新模板没有版本 %d", "The new template has no version %d", toVer)
	}
	if to.API > 0 {
		need, err := VersionMinAPI(ctx, to, toVer)
		if err != nil {
			return nil, err
		}
		if need > to.API {
			return nil, i18n.Errorf("新模板 v%s 需要能力版本 %d 的 Annulo，这台电脑上的是 %d：先更新 Annulo", "The new template v%s needs Annulo capability version %d; this computer has %d. Update Annulo first", vlabel(to.Site, toVer), need, to.API)
		}
	}
	if _, err := commit(dir, "换模板前的改动"); err != nil {
		return nil, err
	}
	// 先认清项目基于旧模板哪一版：合并时以它为共同祖先
	base := Base(dir)
	if base == 0 {
		fvs, err := Versions(ctx, from)
		if err != nil {
			return nil, err
		}
		if base, err = bootstrap(ctx, dir, from, fvs); err != nil {
			return nil, err
		}
	}
	tree, err := snapshotTree(ctx, dir, to, toVer)
	if err != nil {
		return nil, err
	}
	parent, _ := git(dir, "rev-parse", templateBranch)
	if _, err := commitTemplate(dir, tree, strings.TrimSpace(parent), *target, to.Site); err != nil {
		return nil, err
	}
	res := &UpgradeResult{From: base, To: toVer}
	before, _ := git(dir, "rev-parse", "HEAD")
	if _, err := git(dir, "merge", "--no-ff", "-m", fmt.Sprintf("换模板：%s v%s → %s v%s", from.Site, vlabel(from.Site, base), to.Site, vlabel(to.Site, toVer)), templateBranch); err != nil {
		out, _ := git(dir, "diff", "--name-only", "-z", "--diff-filter=U")
		res.Conflicts = splitZ(out)
		if len(res.Conflicts) == 0 {
			git(dir, "merge", "--abort")
			return nil, err
		}
		res.Status = "conflict"
		return res, nil
	}
	out, _ := git(dir, "diff", "--name-only", "-z", strings.TrimSpace(before), "HEAD")
	res.Files = splitZ(out)
	res.Status = "merged"
	return res, nil
}

// bootstrap：老项目没有 template 分支，先认出它基于模板哪个版本：
//   - 历史里有和某个版本完全一样的提交，就是那个版本（取最新的）。复制出来的项目第一个提交就是；
//     模板当初从某个项目同步出去的，那个项目历史里也有；
//   - 都对不上，取和项目第一个提交差异最少的版本。
//
// 把它记成 template 分支的起点，并在 main 上记一次「已经包含它」的合并（-s ours，不改任何文件）。
func bootstrap(ctx context.Context, dir string, t Template, vs []Version) (int, error) {
	out, err := git(dir, "rev-list", "--max-count=200", "HEAD")
	if err != nil {
		return 0, err
	}
	commits := strings.Fields(out)
	if len(commits) == 0 {
		return 0, i18n.New("项目的 git 还没有提交", "The project's git has no commits yet")
	}
	first := commits[len(commits)-1]
	best, bestDiff, bestTree := 0, -1, ""
	var ver Version
	for i, v := range vs {
		if i >= 5 { // 只看最近几个版本
			break
		}
		tree, err := snapshotTree(ctx, dir, t, v.No)
		if err != nil {
			return 0, err
		}
		if same(dir, tree, commits) {
			best, bestDiff, bestTree, ver = v.No, 0, tree, v
			break
		}
		if n := len(changed(dir, tree, first)); bestDiff < 0 || n < bestDiff {
			best, bestDiff, bestTree, ver = v.No, n, tree, v
		}
	}
	if _, err := commitTemplate(dir, bestTree, "", ver, t.Site); err != nil {
		return 0, err
	}
	if _, err := git(dir, "merge", "-s", "ours", "--no-ff", "--allow-unrelated-histories", "-m", fmt.Sprintf("记录模板基准：v%s", vlabel(t.Site, best)), templateBranch); err != nil {
		return 0, err
	}
	return best, nil
}

// changed 是模板快照 tree 和提交 c 之间不同的文件（不算 localOnly）。
func changed(dir, tree, c string) []string {
	out, _ := git(dir, append([]string{"diff", "--name-only", "-z", tree, c, "--"}, localOnly...)...)
	return splitZ(out)
}

// localOnly 是比较项目提交和模板快照时不算的路径：项目自己加的 .gitignore / .creghtignore，
// 以及 creght CLI 拉取时在本地生成的 types/（不是站点文件，版本快照里没有）。
var localOnly = []string{".", ":(exclude).gitignore", ":(exclude).creghtignore", ":(exclude)types"}

// same：commits 里有没有和模板快照 tree 完全一样的提交。
func same(dir, tree string, commits []string) bool {
	for _, c := range commits {
		if _, err := git(dir, append([]string{"diff", "--quiet", tree, c, "--"}, localOnly...)...); err == nil {
			return true
		}
	}
	return false
}

// advance：项目里已经有了基准之后某个版本的全部内容（模板是从这个项目搬的改动，或者手动同步过），
// 就先把基准推到那个版本（-s ours 记一次，不改文件）。不然那一版的改动在两边各算一次，
// 后面的版本再改同一处就会冲突。只看 (base, to] 里最近几个版本，取最新能对上的。
func advance(ctx context.Context, dir string, t Template, vs []Version, base, to int) (int, error) {
	out, err := git(dir, "rev-list", "--max-count=200", "HEAD", "^"+templateBranch)
	if err != nil {
		return base, err
	}
	commits := strings.Fields(out)
	if len(commits) == 0 {
		return base, nil
	}
	checked := 0
	for _, v := range vs {
		if v.No <= base || v.No > to {
			continue
		}
		if checked++; checked > 5 {
			break
		}
		tree, err := snapshotTree(ctx, dir, t, v.No)
		if err != nil {
			return base, err
		}
		if !same(dir, tree, commits) {
			continue
		}
		parent, _ := git(dir, "rev-parse", templateBranch)
		if _, err := commitTemplate(dir, tree, strings.TrimSpace(parent), v, t.Site); err != nil {
			return base, err
		}
		if _, err := git(dir, "merge", "-s", "ours", "--no-ff", "-m", fmt.Sprintf("记录模板基准：v%s（项目里已经有这一版的内容）", vlabel(t.Site, v.No)), templateBranch); err != nil {
			return base, err
		}
		return v.No, nil
	}
	return base, nil
}

// snapshotTree 把模板版本 n 拉到 t.Cache，写成项目仓库里的一个 tree（用临时 index，不碰工作目录）。
func snapshotTree(ctx context.Context, dir string, t Template, n int) (string, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedVersion(t.Cache) != n {
		if err := pullVersion(ctx, t, n); err != nil {
			return "", err
		}
	}
	idx := filepath.Join(dir, ".git", "shuttle-template.index")
	os.Remove(idx)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	// --force：缓存里就是模板这一版的全部文件，模板自己带的 .gitignore 管的是用它建出来的项目，不该挡住模板文件；
	// 而且排除的 .creght、AGENTS.md 正好被它忽略时，不加 -f 的 git add 会直接报错退出
	if _, err := gitEnv(dir, env, "--work-tree="+t.Cache, "add", "-A", "--force", "--", ".", ":(exclude).creght", ":(exclude).git", ":(exclude)AGENTS.md"); err != nil {
		return "", err
	}
	tree, err := gitEnv(dir, env, "write-tree")
	return strings.TrimSpace(tree), err
}

func commitTemplate(dir, tree, parent string, v Version, site string) (string, error) {
	msg := fmt.Sprintf("模板 v%s", vlabel(site, v.No))
	if v.Note != "" {
		msg += "：" + v.Note
	}
	msg += fmt.Sprintf("\n\n模板版本: %d", v.No)
	if site != "" {
		msg += "\n模板站点: " + site
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	sha, err := git(dir, args...)
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	_, err = git(dir, "update-ref", templateBranch, sha)
	return sha, err
}

// Conflicts 是一次还没完成的模板升级里、还留着冲突标记的文件；没有进行中的合并返回 nil。
func Conflicts(dir string) (merging bool, files []string) {
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err != nil {
		return false, nil
	}
	return true, conflictMarked(dir)
}

func splitZ(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// 恢复模板文件：项目被改坏了（助手改错、试模板改动时带进来的），用户在设置里勾几个文件换回模板的样子。
// 这里只换回 template 分支记的那一版，单独一个提交（能撤销）；要换成最新版，调用方接着 Upgrade：
// 这些文件这时和基准一样，合并直接取模板新版。

// ModifiedFile 是项目里和模板（template 分支那一版）不一样的一个文件。
type ModifiedFile struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted,omitempty"` // 项目里删掉了
}

// restorePrefix 是恢复提交的说明开头，撤销时认它
const restorePrefix = "恢复模板文件："

// Modified 是项目（含没提交的改动）里改过、删掉的模板文件；项目自己加的文件不算。还没有 template 分支返回 nil。
func Modified(dir string) ([]ModifiedFile, error) {
	if Base(dir) == 0 {
		return nil, nil
	}
	out, err := git(dir, append([]string{"diff", "--no-renames", "--name-status", "-z", "--diff-filter=MDT", templateBranch, "--"}, localOnly...)...)
	if err != nil {
		return nil, err
	}
	parts := splitZ(out)
	files := []ModifiedFile{}
	for i := 0; i+1 < len(parts); i += 2 {
		files = append(files, ModifiedFile{Path: parts[i+1], Deleted: parts[i] == "D"})
	}
	return files, nil
}

// Restore 把 files（必须在 Modified 里）换回 template 分支那一版，先提交手上的改动，再单独提交一次，返回这次提交。
// 调用方保证助手没在跑。
func Restore(dir string, files []string) (string, error) {
	if len(files) == 0 {
		return "", i18n.New("没有选要恢复的文件", "No files selected to restore")
	}
	if err := Ensure(dir); err != nil {
		return "", err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		return "", i18n.New("模板升级还有冲突没解决完：先解决冲突再恢复", "The template upgrade still has unresolved conflicts: resolve them before restoring")
	}
	mod, err := Modified(dir)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, f := range mod {
		known[f.Path] = true
	}
	for _, f := range files {
		if !known[f] {
			return "", i18n.Errorf("%s 和模板一样，或者不是模板里的文件", "%s matches the template or isn't a template file", f)
		}
	}
	if _, err := commit(dir, "恢复模板文件前的改动"); err != nil {
		return "", err
	}
	if _, err := git(dir, append([]string{"--literal-pathspecs", "checkout", templateBranch, "--"}, files...)...); err != nil {
		return "", err
	}
	if _, err := commit(dir, restorePrefix+summary(files)); err != nil {
		return "", err
	}
	sha, err := git(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// UndoRestore 撤销一次恢复：那次恢复换掉的文件改回恢复之前的样子（之后升级又改过也一样），提交一次。
func UndoRestore(dir, sha string) ([]string, error) {
	if err := Ensure(dir); err != nil {
		return nil, err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		return nil, i18n.New("模板升级还有冲突没解决完：先解决冲突再撤销", "The template upgrade still has unresolved conflicts: resolve them before undoing")
	}
	msg, err := git(dir, "log", "-1", "--format=%s", sha, "--")
	if err != nil || !strings.HasPrefix(msg, restorePrefix) {
		return nil, i18n.New("这不是一次恢复模板文件的提交", "That commit isn't a template-file restore")
	}
	if _, err := git(dir, "merge-base", "--is-ancestor", sha, "HEAD"); err != nil {
		return nil, i18n.New("这次恢复不在项目当前的历史里", "That restore isn't in the project's current history")
	}
	out, err := git(dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", sha)
	if err != nil {
		return nil, err
	}
	files := splitZ(out)
	if _, err := commit(dir, "撤销恢复模板文件前的改动"); err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := git(dir, "cat-file", "-e", sha+"^:"+f); err == nil {
			if _, err := git(dir, "--literal-pathspecs", "checkout", sha+"^", "--", f); err != nil {
				return nil, err
			}
		} else if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if _, err := commit(dir, "撤销"+restorePrefix+summary(files)); err != nil {
		return nil, err
	}
	return files, nil
}

// summary 是提交说明里列的文件：多了只列前几个
func summary(files []string) string {
	if len(files) <= 3 {
		return strings.Join(files, "、")
	}
	return fmt.Sprintf("%s 等 %d 个文件", strings.Join(files[:3], "、"), len(files))
}
