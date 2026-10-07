package wsgit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"
)

// 插件（docs/plugins.md）：装进项目 plugins/<id>/ 的一包文件，有自己的版本，和模板走同一套升级。
// 每个插件一条分支 plugin/<id>，每个提交是插件的一个版本，树里只有 plugins/<id>/ 下的文件；main 从它合并：
// 安装是第一次合并（没有共同祖先），升级把新版本的快照接在分支上再合并，项目里改过插件文件的地方保留，两边改了同一处才冲突。
// 插件来源用 git 模板的那套（gitsource.go）：<仓库>#<子目录>，版本是 semver tag，Template.Site 写成 git:<仓库>#<子目录>。

func pluginBranch(id string) string { return "refs/heads/plugin/" + id }

var (
	pluginVersionTrailer = regexp.MustCompile(`(?m)^插件版本: (\d+)$`)
	pluginSiteTrailer    = regexp.MustCompile(`(?m)^插件来源: (\S+)$`)
)

// PluginBase 是项目装的插件 id 是第几版（plugin/<id> 分支最新提交记的）；没装过返回 0。
func PluginBase(dir, id string) int {
	msg, err := git(dir, "log", "-1", "--format=%B", pluginBranch(id), "--")
	if err != nil {
		return 0
	}
	if m := pluginVersionTrailer.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// PluginSource 是插件 id 装的时候记的来源（git:<仓库>#<子目录>）；没记过返回 ""。
func PluginSource(dir, id string) string {
	msg, err := git(dir, "log", "-1", "--format=%B", pluginBranch(id), "--")
	if err != nil {
		return ""
	}
	if m := pluginSiteTrailer.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

// PluginID 是插件来源对应的插件 id：子目录（或仓库）的最后一段。
func PluginID(site string) (string, error) {
	repo, sub := ParseGitSite(site)
	name := sub
	if name == "" {
		name = strings.TrimSuffix(repo, ".git")
	}
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	if !plugin.IDRe.MatchString(name) {
		return "", i18n.Errorf("插件目录名 %q 不能当插件 id：只能用小写字母和数字，2~20 个字", "The plugin directory name %q can't be a plugin id: lowercase letters and digits only, 2–20 characters", name)
	}
	return name, nil
}

// PluginVersionMeta 是插件第 n 版的 plugin.json（拉到 t.Cache 读）。
func PluginVersionMeta(ctx context.Context, t Template, id string, n int) (plugin.Meta, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedVersion(t.Cache) != n {
		if err := pullVersion(ctx, t, n); err != nil {
			return plugin.Meta{ID: id}, err
		}
	}
	m, err := plugin.ReadMeta(t.Cache, id)
	if os.IsNotExist(err) {
		return m, i18n.Errorf("%s 的 v%s 里没有 %s：不是插件", "%s v%s has no %s: it isn't a plugin", t.Site, vlabel(t.Site, n), plugin.MetaFile)
	}
	return m, err
}

// UpgradePlugin 把插件 id 装上、或升级到 t 的 to 版本（0 表示最新）。调用方保证这时没有别人在改工作目录（助手没在跑）。
// 有冲突时合并停在进行中，和模板升级一样交给助手解决。
func UpgradePlugin(ctx context.Context, dir, id string, t Template, to int) (*UpgradeResult, error) {
	if !plugin.IDRe.MatchString(id) {
		return nil, i18n.Errorf("插件 id 不对：%q", "Invalid plugin id: %q", id)
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
		return nil, i18n.New("上一次升级还有冲突没解决完：让助手解决冲突后再装插件", "The last upgrade still has unresolved conflicts: have the assistant resolve them, then install the plugin")
	}
	vs, err := Versions(ctx, t)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, i18n.New("插件还没有发过版本", "The plugin has no published versions yet")
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
		return nil, i18n.Errorf("插件没有版本 %s", "The plugin has no version %s", VersionLabel(t.Site, to))
	}
	meta, err := PluginVersionMeta(ctx, t, id, to)
	if err != nil {
		return nil, err
	}
	if t.API > 0 && meta.MinAPI > t.API {
		return nil, i18n.Errorf("插件 %s %s 需要能力版本 %d 的 Annulo，这台电脑上的是 %d：先更新 Annulo", "Plugin %s %s needs Annulo capability version %d; this computer has %d. Update Annulo first", id, VersionLabel(t.Site, to), meta.MinAPI, t.API)
	}
	if _, err := commit(dir, "装插件前的改动"); err != nil {
		return nil, err
	}

	base := PluginBase(dir, id)
	res := &UpgradeResult{From: base, To: to}
	if base == to {
		res.Status = "up_to_date"
		return res, nil
	}
	tree, err := snapshotTree(ctx, dir, t, to)
	if err != nil {
		return nil, err
	}
	if tree, err = prefixTree(dir, tree, plugin.Dir+"/"+id+"/"); err != nil {
		return nil, err
	}
	parent, _ := git(dir, "rev-parse", "--verify", "-q", pluginBranch(id))
	parent = strings.TrimSpace(parent)
	if base == 0 {
		parent = "" // 卸载过又装：从头接一条新的，和以前那条无关
	}
	msg := fmt.Sprintf("插件 %s %s", id, VersionLabel(t.Site, to))
	if target.Note != "" {
		msg += "：" + target.Note
	}
	msg += fmt.Sprintf("\n\n插件版本: %d\n插件来源: %s", to, t.Site)
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	sha, err := git(dir, args...)
	if err != nil {
		return nil, err
	}
	if _, err := git(dir, "update-ref", pluginBranch(id), strings.TrimSpace(sha)); err != nil {
		return nil, err
	}

	before, _ := git(dir, "rev-parse", "HEAD")
	mergeArgs := []string{"merge", "--no-ff"}
	title := fmt.Sprintf("升级插件 %s：%s → %s", id, VersionLabel(t.Site, base), VersionLabel(t.Site, to))
	if base == 0 {
		mergeArgs = append(mergeArgs, "--allow-unrelated-histories")
		title = fmt.Sprintf("安装插件 %s %s", id, VersionLabel(t.Site, to))
	}
	if _, err := git(dir, append(mergeArgs, "-m", title, pluginBranch(id))...); err != nil {
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

// prefixTree 把 tree 整个放到 prefix 下（plugins/<id>/），返回新的 tree。用临时 index，不碰工作目录。
func prefixTree(dir, tree, prefix string) (string, error) {
	idx := filepath.Join(dir, ".git", "annulo-plugin.index")
	os.Remove(idx)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, "read-tree", "--prefix="+prefix, tree); err != nil {
		return "", err
	}
	out, err := gitEnv(dir, env, "write-tree")
	return strings.TrimSpace(out), err
}

// RemovePlugin 卸载插件：删掉 plugins/<id>/、单独提交一次，再删 plugin/<id> 分支。表里的数据、user/plugins/<id>/ 都不动。
// 调用方保证助手没在跑。
func RemovePlugin(dir, id string) error {
	if !plugin.IDRe.MatchString(id) {
		return i18n.Errorf("插件 id 不对：%q", "Invalid plugin id: %q", id)
	}
	if err := Ensure(dir); err != nil {
		return err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		return i18n.New("升级还有冲突没解决完：先解决冲突再卸载插件", "An upgrade still has unresolved conflicts: resolve them before removing the plugin")
	}
	if _, err := commit(dir, "卸载插件前的改动"); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, plugin.Dir, id)); err != nil {
		return err
	}
	os.Remove(filepath.Join(dir, plugin.Dir)) // 最后一个插件卸掉了：空的 plugins/ 也删掉（不空删不掉）
	if _, err := commit(dir, "卸载插件 "+id); err != nil {
		return err
	}
	git(dir, "update-ref", "-d", pluginBranch(id))
	return nil
}
