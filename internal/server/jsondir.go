package server

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/annulo/annulo/internal/plugin"
)

// 项目里「一个对象一个文件」的声明目录（tables/<key>.json、schedules/<id>.json）：文件名就是 key。
// 拆成单个文件，行业模板能按文件增删覆盖，项目升级时的三方合并也按文件算，不会因为同一个大 JSON 里各改各的冲突。

type jsonFile struct {
	Plugin string // 插件 id（plugins/<id>/ 下的）；项目自己的是空
	Key    string // 文件名去掉 .json（插件的不带插件 id，调用方按种类拼：表 social_posts、定时任务 social/x.collect）
	Rel    string // 相对项目根目录的路径，报错时给人看
	Data   []byte
}

// readJSONDir 读项目 dir/ 和每个插件 plugins/<id>/dir/ 下的 *.json：项目的在前，各自按 key 排好。一个文件都没有返回 nil, nil。
func readJSONDir(root, dir string) ([]jsonFile, error) {
	var out []jsonFile
	for _, id := range plugin.Sources(root) {
		entries, err := os.ReadDir(filepath.Join(plugin.Root(root, id), dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var files []jsonFile
		for _, e := range entries {
			key, ok := strings.CutSuffix(e.Name(), ".json")
			if e.IsDir() || !ok || key == "" || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			rel := plugin.Rel(id, dir) + "/" + e.Name()
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			files = append(files, jsonFile{Plugin: id, Key: key, Rel: rel, Data: b})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })
		out = append(out, files...)
	}
	return out, nil
}

// dirSignature 项目 dir/ 和插件 plugins/<id>/dir/ 里 *.json 的文件名、大小、修改时间拼成的指纹：
// 改了哪个文件、加删了文件、装卸了插件都会变（目录自己的修改时间只管加删）。目录都不存在返回 ""。
func dirSignature(root, dir string) string {
	var b strings.Builder
	for _, id := range plugin.Sources(root) {
		entries, err := os.ReadDir(filepath.Join(plugin.Root(root, id), dir))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "dir %s;", id)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s:%d:%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	return b.String()
}

// splitNameAction 拆 local/tasks/<id>/<动作>、local/schedules/<id>/<动作> 这类路由：插件的 id 带一段 /（social/write-x），
// 第一段是装了的插件就连着下一段算 id。项目自己的 id 里没有 /。
func splitNameAction(root, rest string) (id, action string) {
	first, after, _ := strings.Cut(rest, "/")
	if after != "" && slices.Contains(plugin.IDs(root), first) {
		second, action, _ := strings.Cut(after, "/")
		return first + "/" + second, action
	}
	return first, after
}
