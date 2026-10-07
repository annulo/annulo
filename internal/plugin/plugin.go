// Package plugin 认项目里装的插件（docs/plugins.md）：一个项目 = 一个模板 + 若干插件。
//
// 插件是一包项目文件，放在 plugins/<id>/，里面的目录和项目根目录一样（local/、tables/、tasks/、prompts/、schedules/、skills/、components/），
// 有 plugins/<id>/plugin.json 才算装了。插件里的东西在项目里都带插件 id，和项目自己的、别的插件的不会撞：
//
//	本机函数 plugins/social/local/x.ts 的 publish → social/x.publish
//	任务     plugins/social/tasks/write-x.md       → social/write-x（写法 plugins/social/prompts/write-x.md，用户改的 user/plugins/social/prompts/write-x.md）
//	定时任务 plugins/social/schedules/x.collect.json → social/x.collect
//	表       plugins/social/tables/posts.json      → social_posts
//
// 插件只读自己目录里的东西，不 import 项目的文件；用户的定制放 user/plugins/<id>/，插件不写那里。
package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// Dir 是项目里放插件的目录。
	Dir = "plugins"
	// MetaFile 是插件的说明（id、名字、简介、min_annulo_api），有它才算装了插件。
	MetaFile = "plugin.json"
)

// IDRe：插件 id 会拼进函数名、任务 id、表名、对话 id，只用小写字母和数字（不带 _ 和 -，表名前缀 <id>_ 才拆得开）。
var IDRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,19}$`)

// IDs 是项目装的插件（plugins/<id>/plugin.json 存在的），按 id 排。
func IDs(root string) []string {
	ents, _ := os.ReadDir(filepath.Join(root, Dir))
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !IDRe.MatchString(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, Dir, e.Name(), MetaFile)); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Root 是插件的目录（相对 root）；id 空是项目自己。
func Root(root, id string) string {
	if id == "" {
		return root
	}
	return filepath.Join(root, Dir, id)
}

// Rel 是插件里一个目录相对项目根目录的路径（报错、界面给人看）：Rel("social", "tables") = plugins/social/tables。
func Rel(id, sub string) string {
	if id == "" {
		return sub
	}
	return Dir + "/" + id + "/" + sub
}

// Split 拆开带插件 id 的名字：social/x → (social, x)；不带 / 的是项目自己的：x → ("", x)。
// ok=false：有 / 但插件 id 不对，或者 / 不止一个。
func Split(name string) (id, rest string, ok bool) {
	id, rest, found := strings.Cut(name, "/")
	if !found {
		return "", name, true
	}
	if !IDRe.MatchString(id) || rest == "" || strings.Contains(rest, "/") {
		return "", "", false
	}
	return id, rest, true
}

// Name 拼出项目里的名字：插件的带 <id>/。
func Name(id, rest string) string {
	if id == "" {
		return rest
	}
	return id + "/" + rest
}

// ChatKey 把名字里的 / 换掉：对话 id、文件名里不能有 /（social/write-x → social__write-x）。
func ChatKey(name string) string { return strings.ReplaceAll(name, "/", "__") }

// TableKey 是插件的表在项目里的名字：<id>_<key>。
func TableKey(id, key string) string {
	if id == "" {
		return key
	}
	return id + "_" + key
}

// UserDir 是用户对插件的定制放在哪（相对项目根目录）：user/plugins/<id>。
func UserDir(id string) string { return "user/" + Dir + "/" + id }

// Sources 是项目自己和每个装了的插件：先项目（id 空），再按插件 id 排。读 tables/、schedules/ 这类目录时挨个看。
func Sources(root string) []string {
	return append([]string{""}, IDs(root)...)
}

// Meta 是 plugin.json：{"name": …, "description": …, "min_annulo_api": N}，name、description 写字符串或 {"zh": …, "en": …}。
type Meta struct {
	ID     string
	Name   [2]string // 中文、英文；没写用 id
	Desc   [2]string
	MinAPI int
}

// ReadMeta 读插件的 plugin.json；dir 是插件目录（装在项目里的 plugins/<id>，或者某一版的快照）。
func ReadMeta(dir, id string) (Meta, error) {
	m := Meta{ID: id, Name: [2]string{id, id}}
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return m, err
	}
	var v struct {
		Name         json.RawMessage `json:"name"`
		Description  json.RawMessage `json:"description"`
		MinAnnuloAPI int             `json:"min_annulo_api"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return m, err
	}
	if n := localized(v.Name); n[0] != "" {
		m.Name = n
	}
	m.Desc = localized(v.Description)
	m.MinAPI = v.MinAnnuloAPI
	return m, nil
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
		zh = en
	}
	if en == "" {
		en = zh
	}
	return [2]string{zh, en}
}
