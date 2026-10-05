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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.autocrlf=false"}, args...)...)
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
	name, note, date string
	no               int
}

// gitTags 是镜像里的 semver tag，新的在前。
func gitTags(ctx context.Context, repo, dir string) ([]gitTag, error) {
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
		no, ok := SemverNo(f[0])
		if !ok {
			continue
		}
		note := f[2]
		if i := strings.Index(note, "-----BEGIN PGP SIGNATURE-----"); i >= 0 {
			note = note[:i]
		}
		tags = append(tags, gitTag{name: f[0], date: f[1], note: strings.TrimSpace(note), no: no})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].no > tags[j].no })
	return tags, nil
}

func versionsGit(ctx context.Context, t Template) ([]Version, error) {
	repo, _ := ParseGitSite(t.Site)
	dir, err := mirror(ctx, t)
	if err != nil {
		return nil, err
	}
	tags, err := gitTags(ctx, repo, dir)
	if err != nil {
		return nil, err
	}
	vs := make([]Version, 0, len(tags))
	for _, g := range tags {
		vs = append(vs, Version{No: g.no, Label: g.name, Note: g.note, CreatedAt: g.date})
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
	tags, err := gitTags(ctx, repo, dir)
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
	tags, err := gitTags(ctx, repo, dir)
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
