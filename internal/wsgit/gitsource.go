package wsgit

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/annulo/annulo/internal/i18n"
)

// git 模板源（docs/annulo-plan.md 第 3 步）：模板是一个 git 仓库（可以只用其中一个子目录），版本是 semver tag，
// 每个 tag 上是拼好的成品（Annulo 不做拼装）。和 creght 模板走同一套升级：template 分支、三方合并、user/ 不冲突。
//
//   - 模板站点（Template.Site、template 分支的「模板站点」trailer）写成 git:<仓库>#<子目录>，子目录可以没有；
//   - 项目里记的模板版本是整数：semver 换算成 major*1000000 + minor*1000 + patch（v1.2.3 → 1002003），界面显示原来的 tag（Version.Label）；
//   - 只认 vX.Y.Z / X.Y.Z，预发布（-beta.1）和别的 tag 不列；
//   - 一个仓库放多个模板、插件时，每个子目录可以自己发版本：tag 写成 <子目录>/vX.Y.Z（像 Go 一个仓库多个 module），
//     有这种 tag 的子目录只认它们，没有的用仓库的 vX.Y.Z（见 gitTags）；
//   - 仓库镜像成一个 bare 仓库放在快照目录旁边（<Cache>.git），每次查版本 fetch 一次 tag；认证用本机 git 自己的（credential helper、SSH key），不弹输入框。

// GitPrefix 是 git 模板站点的前缀。
const GitPrefix = "git:"

// IsGitSite：模板站点是不是 git 仓库。
func IsGitSite(site string) bool { return strings.HasPrefix(site, GitPrefix) }

// GitSite 拼出 git 模板站点：git:<仓库>[#<子目录>]。
func GitSite(repo, sub string) string {
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return GitPrefix + repo
	}
	return GitPrefix + repo + "#" + sub
}

// ParseGitSite 拆出仓库和子目录。
func ParseGitSite(site string) (repo, sub string) {
	repo, sub, _ = strings.Cut(strings.TrimPrefix(site, GitPrefix), "#")
	return repo, strings.Trim(sub, "/")
}

var semverTag = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// SemverNo 把 semver tag 换成项目里记的整数版本号；不是 X.Y.Z（带预发布、不是 semver）返回 false。
func SemverNo(tag string) (int, bool) {
	m := semverTag.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	ma, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	pa, _ := strconv.Atoi(m[3])
	if mi > 999 || pa > 999 {
		return 0, false
	}
	return ma*1_000_000 + mi*1_000 + pa, true
}

// VersionLabel 是给人看的版本：git 模板把整数换回 semver（1002003 → v1.2.3），creght 模板就是版本号。
func VersionLabel(site string, n int) string {
	if IsGitSite(site) {
		return "v" + strconv.Itoa(n/1_000_000) + "." + strconv.Itoa(n/1_000%1_000) + "." + strconv.Itoa(n%1_000)
	}
	return strconv.Itoa(n)
}

// vlabel 是提交说明、报错里 v 后面的版本（v1.2.3 / v7）。
func vlabel(site string, n int) string { return strings.TrimPrefix(VersionLabel(site, n), "v") }

var mirrorMu sync.Map // 镜像目录 → *sync.Mutex：同一个镜像同一时间只 fetch 一次

func mirrorLock(dir string) func() {
	v, _ := mirrorMu.LoadOrStore(dir, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// gitRun 在 ctx 下跑 git，不弹认证输入框；出错时把 stderr 换成看得懂的话。
func gitRun(ctx context.Context, repo string, args ...string) ([]byte, error) {
	// Git for Windows 默认 core.autocrlf=true，git archive 会把模板文件改成 CRLF，和仓库里的不一样
	bin, err := gitBin()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-c", "core.autocrlf=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, gitSourceError(repo, errb.String(), err)
	}
	return out.Bytes(), nil
}

func gitSourceError(repo, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "authentication failed"), strings.Contains(low, "could not read username"), strings.Contains(low, "permission denied"),
		strings.Contains(low, "terminal prompts disabled"), strings.Contains(low, "403"):
		return i18n.Errorf("没有权限读模板仓库 %s：私有仓库要先在这台电脑的 git 里登录（credential helper 或 SSH key）", "No access to the template repository %s: for a private repository, sign in with git on this computer first (credential helper or SSH key)", repo)
	case strings.Contains(low, "repository not found"), strings.Contains(low, "not found"), strings.Contains(low, "does not appear to be a git repository"):
		return i18n.Errorf("找不到模板仓库 %s：检查地址对不对；私有仓库没登录时也会这样报", "Template repository %s not found: check the address; a private repository you're not signed in to also shows this", repo)
	case strings.Contains(low, "not a valid object name"), strings.Contains(low, "pathspec"):
		return i18n.Errorf("模板仓库 %s 里没有这个子目录：检查 # 后面写的目录名（要和仓库里的大小写一样）", "The template repository %s has no such subdirectory: check the directory name after # (case matters)", repo)
	case strings.Contains(low, "could not resolve host"), strings.Contains(low, "unable to access"), strings.Contains(low, "timed out"):
		return i18n.Errorf("连不上模板仓库 %s：%s", "Can't reach the template repository %s: %s", repo, msg)
	}
	if msg == "" {
		msg = err.Error()
	}
	return i18n.Errorf("读模板仓库 %s 失败：%s", "Reading the template repository %s failed: %s", repo, msg)
}

// mirror 是 t 的 bare 镜像目录，拉一次最新的 tag。
func mirror(ctx context.Context, t Template) (string, error) {
	if err := Available(); err != nil {
		return "", err
	}
	repo, _ := ParseGitSite(t.Site)
	dir := strings.TrimRight(t.Cache, string(os.PathSeparator)) + ".git"
	unlock := mirrorLock(dir)
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if _, err := gitRun(ctx, repo, "init", "-q", "--bare", dir); err != nil {
			return "", err
		}
	}
	if _, err := gitRun(ctx, repo, "--git-dir="+dir, "fetch", "-q", "--prune", "--force", "--no-tags", repo, "+refs/tags/*:refs/tags/*"); err != nil {
		return "", err
	}
	return dir, nil
}

type gitTag struct {
	name, note, date string // name 是完整的 tag 名（social/v0.8.0），git 命令用它
	label            string // 去掉目录前缀的版本（v0.8.0），给人看
	dir              string // 带目录前缀的 tag 的目录（social），不带前缀的是空
	no               int
}

// allTags 是镜像里全部的 semver tag：不带前缀的 vX.Y.Z，和一个仓库放多个模板、插件时每个目录自己的 <目录>/vX.Y.Z（像 Go 一个仓库多个 module），新的在前。
func allTags(ctx context.Context, repo, dir string) ([]gitTag, error) {
	out, err := gitRun(ctx, repo, "--git-dir="+dir, "for-each-ref", "refs/tags", "--format=%(refname:strip=2)%00%(creatordate:iso-strict)%00%(contents)%00%00")
	if err != nil {
		return nil, err
	}
	var tags []gitTag
	for _, rec := range strings.Split(string(out), "\x00\x00") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 3)
		if len(f) < 3 {
			continue
		}
		d, label := "", f[0]
		if i := strings.LastIndex(f[0], "/"); i >= 0 {
			d, label = f[0][:i], f[0][i+1:]
		}
		no, ok := SemverNo(label)
		if !ok {
			continue
		}
		note := f[2]
		if i := strings.Index(note, "-----BEGIN PGP SIGNATURE-----"); i >= 0 {
			note = note[:i]
		}
		tags = append(tags, gitTag{name: f[0], label: label, dir: d, date: f[1], note: strings.TrimSpace(note), no: no})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].no > tags[j].no })
	return tags, nil
}

// gitTags 是子目录 sub 的版本，新的在前：有 <sub>/vX.Y.Z 的 tag 就用这些（这个目录自己发版本），
// 加上仓库的 vX.Y.Z 里比它自己最早一版还低的（改成自己打 tag 之前的历史：装着老版本的项目升级要拿老版本的文件做三方合并）；
// 没有就用仓库的 vX.Y.Z（整个仓库一个版本：公开模板仓库、老的插件仓库）。
func gitTags(ctx context.Context, repo, dir, sub string) ([]gitTag, error) {
	all, err := allTags(ctx, repo, dir)
	if err != nil {
		return nil, err
	}
	var own, bare []gitTag
	for _, g := range all {
		switch {
		case sub != "" && g.dir == sub:
			own = append(own, g)
		case g.dir == "":
			bare = append(bare, g)
		}
	}
	if len(own) == 0 {
		return bare, nil
	}
	first := own[len(own)-1].no
	for _, g := range bare {
		if g.no < first {
			own = append(own, g)
		}
	}
	return own, nil
}

func versionsGit(ctx context.Context, t Template) ([]Version, error) {
	repo, sub := ParseGitSite(t.Site)
	dir, err := mirror(ctx, t)
	if err != nil {
		return nil, err
	}
	tags, err := gitTags(ctx, repo, dir, sub)
	if err != nil {
		return nil, err
	}
	vs := make([]Version, 0, len(tags))
	for _, g := range tags {
		vs = append(vs, Version{No: g.no, Label: g.label, Note: g.note, CreatedAt: g.date})
	}
	// 升级说明按语言从最新一版的 changelog.yaml 取（见 changelog.go），没有这个文件就用 tag 的说明
	if len(tags) > 0 {
		if body, err := gitRun(ctx, repo, "--git-dir="+dir, "show", "refs/tags/"+tags[0].name+"^{commit}:"+path.Join(sub, changelogFile)); err == nil {
			localizeNotes(vs, parseChangelog(body, SemverNo))
		}
	}
	return vs, nil
}

// filesGit 是第 n 版（对应的 tag）子目录里的全部文件。
func filesGit(ctx context.Context, t Template, n int) (map[string]string, error) {
	repo, sub := ParseGitSite(t.Site)
	dir, err := mirror(ctx, t)
	if err != nil {
		return nil, err
	}
	tags, err := gitTags(ctx, repo, dir, sub)
	if err != nil {
		return nil, err
	}
	tag := ""
	for _, g := range tags {
		if g.no == n {
			tag = g.name
		}
	}
	if tag == "" {
		return nil, i18n.Errorf("模板仓库 %s 里没有这一版", "The template repository %s has no such version", repo)
	}
	treeish := "refs/tags/" + tag + "^{commit}"
	if sub != "" {
		treeish += ":" + sub
	}
	out, err := gitRun(ctx, repo, "--git-dir="+dir, "archive", "--format=tar", treeish)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[strings.TrimPrefix(h.Name, "./")] = string(b)
	}
	if len(files) == 0 {
		return nil, i18n.Errorf("模板仓库 %s 的 %s 里没有文件（子目录写对了吗？）", "The template repository %s has no files in %s (is the subdirectory right?)", repo, tag)
	}
	return files, nil
}

// TemplateMeta 是模板自己在 annulo.json（或 shuttle.json）里写的名字和简介：字符串，或 {"zh": …, "en": …}。
type TemplateMeta struct {
	Name, Desc [2]string // 中文、英文
}

// GitMeta 读 git 模板最新一版的名字和简介；没写就用仓库（子目录）名。
func GitMeta(ctx context.Context, t Template) (TemplateMeta, error) {
	repo, sub := ParseGitSite(t.Site)
	base := path.Base(strings.TrimSuffix(repo, ".git"))
	if sub != "" {
		base = path.Base(sub)
	}
	meta := TemplateMeta{Name: [2]string{base, base}}
	dir, err := mirror(ctx, t)
	if err != nil {
		return meta, err
	}
	tags, err := gitTags(ctx, repo, dir, sub)
	if err != nil || len(tags) == 0 {
		return meta, err
	}
	for _, f := range []string{"annulo.json", "shuttle.json"} {
		p := f
		if sub != "" {
			p = sub + "/" + f
		}
		out, err := gitRun(ctx, repo, "--git-dir="+dir, "show", "refs/tags/"+tags[0].name+"^{commit}:"+p)
		if err != nil {
			continue
		}
		var v struct {
			Name        json.RawMessage `json:"name"`
			Description json.RawMessage `json:"description"`
		}
		if json.Unmarshal(out, &v) != nil {
			continue
		}
		if n := localized(v.Name); n[0] != "" {
			meta.Name = n
		}
		meta.Desc = localized(v.Description)
		break
	}
	return meta, nil
}

func localized(raw json.RawMessage) [2]string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return [2]string{s, s}
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return [2]string{}
	}
	zh, en := m["zh"], m["en"]
	if zh == "" {
		zh = m["zh-CN"]
	}
	if zh == "" {
		zh = en
	}
	if en == "" {
		en = zh
	}
	return [2]string{zh, en}
}

// CatalogDirs 列出一个仓库最新一版（semver tag）里有 marker 文件的一级子目录：仓库里放了多个模板（或插件）时，
// 每个子目录就是一个（github.com/annulo/templates 的 blank/、creator/；annulo/plugins 的 social/、docs/）。
// 自己发版本的目录（有 <目录>/vX.Y.Z 的 tag）看它自己最新的 tag，别的看仓库最新的 vX.Y.Z。
// t.Site 写仓库本身（git:<仓库>，不带子目录）；markers 任一个在子目录里就算（annulo.json 或老的 shuttle.json）。
func CatalogDirs(ctx context.Context, t Template, markers ...string) ([]string, error) {
	repo, _ := ParseGitSite(t.Site)
	dir, err := mirror(ctx, t)
	if err != nil {
		return nil, err
	}
	tags, err := allTags(ctx, repo, dir)
	if err != nil {
		return nil, err
	}
	own := map[string]string{} // 目录 → 它自己最新的 tag
	bare := ""
	for _, g := range tags {
		if g.dir == "" && bare == "" {
			bare = g.name
		} else if g.dir != "" && !strings.Contains(g.dir, "/") && own[g.dir] == "" {
			own[g.dir] = g.name
		}
	}
	if bare == "" && len(own) == 0 {
		return nil, i18n.Errorf("仓库 %s 还没有发过版本（vX.Y.Z 或 <目录>/vX.Y.Z 的 tag）", "Repository %s has no versions yet (vX.Y.Z or <dir>/vX.Y.Z tags)", repo)
	}
	// 有 marker 的一级子目录：treeish 是一个 tag，only 不空时只看这个目录
	scan := func(tag, only string) ([]string, error) {
		out, err := gitRun(ctx, repo, "--git-dir="+dir, "ls-tree", "-r", "--name-only", "refs/tags/"+tag+"^{commit}")
		if err != nil {
			return nil, err
		}
		var ds []string
		for _, p := range strings.Split(string(out), "\n") {
			d, f, ok := strings.Cut(p, "/")
			if !ok || strings.Contains(f, "/") || !slices.Contains(markers, f) || (only != "" && d != only) {
				continue
			}
			ds = append(ds, d)
		}
		return ds, nil
	}
	seen := map[string]bool{}
	var dirs []string
	for d, tag := range own {
		ds, err := scan(tag, d)
		if err != nil {
			return nil, err
		}
		if len(ds) > 0 {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	if bare != "" {
		ds, err := scan(bare, "")
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if !seen[d] && own[d] == "" {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}
