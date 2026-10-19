package wsgit

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/annulo/annulo/internal/i18n"
)

// 升级说明（changelog.yaml）：模板、插件目录里放一个 changelog.yaml，新的在最上面，每条一个 version 加各语言一段：
//
//	- version: 0.9.0
//	  zh: 发布记录里的账号名能点到「数据表现」
//	  en: Account names in publishing records link to Performance
//
// 界面上的升级说明按当前语言从这里取：没有这种语言用 en，再没有用第一种；文件里没有这一版的用 tag / 版本的说明（只写英文）。
const changelogFile = "changelog.yaml"

// parseChangelog 把 changelog.yaml 解析成 版本号 → 语言 → 说明；版本号用 SemverNo 换算（git 源）或原样的整数（creght 源）。
func parseChangelog(body []byte, no func(string) (int, bool)) map[int]map[string]string {
	var entries []map[string]string
	if yaml.Unmarshal(body, &entries) != nil {
		return nil
	}
	out := map[int]map[string]string{}
	for _, e := range entries {
		n, ok := no(strings.TrimSpace(e["version"]))
		if !ok {
			continue
		}
		delete(e, "version")
		out[n] = e
	}
	return out
}

// pickNote 按当前语言选一段说明：当前语言 → en → 按语言代码排第一的；都没有返回空。
func pickNote(notes map[string]string) string {
	if s := strings.TrimSpace(notes[i18n.Locale()]); s != "" {
		return s
	}
	if s := strings.TrimSpace(notes["en"]); s != "" {
		return s
	}
	keys := make([]string, 0, len(notes))
	for k := range notes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s := strings.TrimSpace(notes[k]); s != "" {
			return s
		}
	}
	return ""
}

// localizeNotes 用 changelog 里当前语言的说明替换版本的说明，没写的保留原来的。
func localizeNotes(vs []Version, log map[int]map[string]string) {
	for i := range vs {
		if s := pickNote(log[vs[i].No]); s != "" {
			vs[i].Note = s
		}
	}
}
