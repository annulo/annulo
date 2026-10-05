package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 项目里「一个对象一个文件」的声明目录（tables/<key>.json、schedules/<id>.json）：文件名就是 key。
// 拆成单个文件，行业模板能按文件增删覆盖，项目升级时的三方合并也按文件算，不会因为同一个大 JSON 里各改各的冲突。

type jsonFile struct {
	Key  string // 文件名去掉 .json
	Rel  string // 相对项目根目录的路径，报错时给人看
	Data []byte
}

// readJSONDir 读目录下的 *.json，按 key 排好。目录不存在返回 nil, nil。
func readJSONDir(root, dir string) ([]jsonFile, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []jsonFile
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || key == "" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, jsonFile{Key: key, Rel: dir + "/" + e.Name(), Data: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// dirSignature 目录里 *.json 的文件名、大小、修改时间拼成的指纹：改了哪个文件、加删了文件都会变（目录自己的修改时间只管加删）。
// 目录不存在返回 ""。
func dirSignature(root, dir string) string {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("dir;")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
		}
	}
	return b.String()
}
