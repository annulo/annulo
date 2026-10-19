package server

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/annulo/annulo/internal/i18n"
)

// 引用：@<表>:<id>#<n>（docs/references.md）。用户在页面上「复制引用」，贴进对话交给助手。
// 表都是项目动态声明的，这里不认识具体业务：表名、标题、文件按表声明和通用规则推出来。
//
//	POST /_shuttle/api/refs/resolve { refs: ["@articles:…", …] } → { list: [{ ref, table, table_name, id, n, title, mime, thumb, missing }] }
//
// 发给助手的消息里有引用时，在后面附一段 <references>（resolveRefsBlock），引用的本机图片同时作为图片附给模型。

var refRe = regexp.MustCompile(`@([a-z][a-z0-9_]*):([A-Za-z0-9_-]+)(?:#(\d+))?`)

// refFile 是记录里的一个文件
type refFile struct {
	URL  string `json:"url"`
	Mime string `json:"mime,omitempty"`
	Name string `json:"name,omitempty"`
}

type refInfo struct {
	Ref       string `json:"ref"`
	Table     string `json:"table"`
	TableName string `json:"table_name"`
	ID        string `json:"id"`
	N         int    `json:"n,omitempty"`
	Title     string `json:"title"`
	Mime      string `json:"mime,omitempty"`
	File      string `json:"file,omitempty"`  // 带 #n 时这个文件的名字（files 里的 name，比如「封面」）
	Thumb     string `json:"thumb,omitempty"` // 页面上显示用的地址（图片、视频）
	Missing   bool   `json:"missing,omitempty"`

	row   map[string]any
	files []refFile // 引用到的文件：带 #n 是那一个，不带是这条的全部
}

// tableMeta 是表声明里引用要用的几项：显示名、标题字段、文件列表字段
type tableMeta struct {
	Name       string          `json:"name"`
	TitleField string          `json:"title_field"`
	FilesField string          `json:"files_field"`
	JSONSchema json.RawMessage `json:"json_schema"`
}

func (s *Server) tableMetaOf(table string) tableMeta {
	defs, _ := s.tableDefs()
	for _, d := range defs {
		if d.Key != table {
			continue
		}
		m := tableMeta{Name: d.Name, JSONSchema: d.JSONSchema}
		// title_field / files_field 不在 tableDef 里：直接读声明文件（插件的表在 plugins/<id>/tables/ 下，读不到就按通用规则）
		for _, p := range tableFiles(s.ws.Dir, table) {
			if b, err := os.ReadFile(p); err == nil {
				var x tableMeta
				if json.Unmarshal(b, &x) == nil {
					m.TitleField, m.FilesField = x.TitleField, x.FilesField
				}
				break
			}
		}
		if m.Name == "" {
			m.Name = table
		}
		return m
	}
	return tableMeta{Name: table}
}

// tableFiles 是这张表可能的声明文件：项目的 tables/<表>.json；插件的表 <插件>_<key> 在 plugins/<插件>/tables/<key>.json
func tableFiles(dir, table string) []string {
	out := []string{filepath.Join(dir, WorkspaceTablesDir, table+".json")}
	if i := strings.Index(table, "_"); i > 0 {
		out = append(out, filepath.Join(dir, "plugins", table[:i], "tables", table[i+1:]+".json"))
	}
	return out
}

// resolveRef 解析一个引用：只认项目声明了的表；找不到的标 missing，不报错。
func (s *Server) resolveRef(ctx context.Context, ref string) refInfo {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return refInfo{Ref: ref, Missing: true}
	}
	r := refInfo{Ref: m[0], Table: m[1], ID: m[2]}
	r.N, _ = strconv.Atoi(m[3])
	meta := s.tableMetaOf(r.Table)
	r.TableName = meta.Name
	if !s.isOpsTable(r.Table) {
		r.Missing = true
		return r
	}
	row, err := localDB{s.cur()}.Get(ctx, r.Table, r.ID)
	if err != nil || row == nil {
		r.Missing = true
		return r
	}
	r.row = row
	r.Title = refTitle(row, meta)
	files := refFiles(row, meta.FilesField)
	if r.N > 0 {
		if r.N > len(files) {
			r.Missing = true
			return r
		}
		files = files[r.N-1 : r.N]
	}
	r.files = files
	if len(files) > 0 {
		f := files[0]
		r.Mime = f.Mime
		if r.N > 0 {
			r.File = f.Name
		}
		if strings.HasPrefix(f.Mime, "image/") || strings.HasPrefix(f.Mime, "video/") {
			r.Thumb = refThumb(f.URL)
		}
	}
	return r
}

// refTitle：表声明的 title_field → title / name / subject / label → 第一个像标题的字符串字段 → 空（页面显示 id）
func refTitle(row map[string]any, meta tableMeta) string {
	pick := func(k string) string {
		v, _ := row[k].(string)
		return strings.TrimSpace(v)
	}
	if meta.TitleField != "" {
		if v := pick(meta.TitleField); v != "" {
			return clip(v, 60)
		}
	}
	for _, k := range []string{"title", "name", "subject", "label"} {
		if v := pick(k); v != "" {
			return clip(v, 60)
		}
	}
	for _, k := range schemaKeys(meta.JSONSchema) {
		if v := pick(k); v != "" && !strings.Contains(v, "://") && !strings.HasPrefix(v, "local:") && len(v) > 1 {
			return clip(oneLine(v), 60)
		}
	}
	return ""
}

// schemaKeys 按声明里的顺序列出字段名（Go 的 map 不保序，按 JSON 原文的顺序读）
func schemaKeys(raw json.RawMessage) []string {
	var s struct {
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil || len(s.Properties) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(s.Properties)))
	if _, err := dec.Token(); err != nil { // {
		return nil
	}
	var out []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		k, _ := t.(string)
		out = append(out, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			break
		}
	}
	return out
}

// refFiles 是这条记录的文件列表：表声明的 files_field → files → images → url / video（单个文件）。
// 列表字段可以是数组，也可以是存成 JSON 字符串的数组；每项是地址字符串或 { url, mime, name }。
func refFiles(row map[string]any, field string) []refFile {
	fields := []string{"files", "images"}
	if field != "" {
		fields = []string{field}
	}
	for _, f := range fields {
		if fs := parseFiles(row[f]); len(fs) > 0 {
			return fs
		}
	}
	for _, f := range []string{"url", "video"} {
		if v, _ := row[f].(string); strings.TrimSpace(v) != "" {
			mt, _ := row["mime"].(string)
			return []refFile{{URL: v, Mime: orMime(mt, v)}}
		}
	}
	return nil
}

func parseFiles(v any) []refFile {
	if s, ok := v.(string); ok {
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil
		}
	}
	list, _ := v.([]any)
	var out []refFile
	for _, it := range list {
		switch x := it.(type) {
		case string:
			if x != "" {
				out = append(out, refFile{URL: x, Mime: orMime("", x)})
			}
		case map[string]any:
			u, _ := x["url"].(string)
			if u == "" {
				continue
			}
			mt, _ := x["mime"].(string)
			name, _ := x["name"].(string)
			out = append(out, refFile{URL: u, Mime: orMime(mt, u), Name: name})
		}
	}
	return out
}

func orMime(mt, u string) string {
	if mt != "" {
		return mt
	}
	path := u
	if p, err := url.Parse(u); err == nil && p.Path != "" {
		path = p.Path
	}
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
}

// refThumb：页面上显示的地址（本机文件走 /_shuttle/files，网址走图片代理）
func refThumb(u string) string {
	if name, ok := strings.CutPrefix(u, "local:"); ok {
		return localFilesURL + name
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return "/_shuttle/img?url=" + url.QueryEscape(u)
	}
	return u
}

// refPath：交给助手的文件位置。本机文件给绝对路径（助手读 Annulo 数据目录里文件的唯一入口，只给被引用的这个），网址原样
func (s *Server) refPath(u string) string {
	if name, ok := strings.CutPrefix(u, "local:"); ok && localFileRe.MatchString(name) {
		if dir := s.localFilesDir(); dir != "" {
			return filepath.Join(dir, name)
		}
	}
	return u
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// resolveRefsBlock 是附在用户消息后面给助手看的解析结果（没有引用返回空），images 是引用到的本机图片（能直接给模型看的那几种）。
func (s *Server) resolveRefsBlock(ctx context.Context, text string) (block string, images []string) {
	seen := map[string]bool{}
	var lines []string
	for _, m := range refRe.FindAllString(text, 20) {
		if seen[m] {
			continue
		}
		seen[m] = true
		r := s.resolveRef(ctx, m)
		if r.Missing {
			lines = append(lines, fmt.Sprintf("%s  %s", r.Ref, i18n.T("找不到这条记录（可能删了）", "Not found (it may have been deleted)")))
			continue
		}
		line := fmt.Sprintf("%s  %s「%s」", r.Ref, r.TableName, r.Title)
		if r.N > 0 && len(r.files) == 1 {
			line += fmt.Sprintf(" 第 %d 个文件%s：%s  %s", r.N, fileLabel(r.files[0]), r.files[0].Mime, s.refPath(r.files[0].URL))
		} else {
			line += "\n" + refFields(r.row)
			for i, f := range r.files {
				line += fmt.Sprintf("\n  文件 #%d%s：%s  %s", i+1, fileLabel(f), f.Mime, s.refPath(f.URL))
			}
		}
		lines = append(lines, line)
		for _, f := range r.files {
			p := s.refPath(f.URL)
			if filepath.IsAbs(p) && refImageExt[strings.ToLower(filepath.Ext(p))] {
				if st, err := os.Stat(p); err == nil && st.Size() <= 20<<20 && len(images) < 8 {
					images = append(images, p)
				}
			}
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "<references>\n" + strings.Join(lines, "\n") + "\n</references>", images
}

func fileLabel(f refFile) string {
	if f.Name == "" {
		return ""
	}
	return "「" + f.Name + "」"
}

var refImageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// refFields：记录里的文字字段，每个截一段（正文这类长的多给一点），要全文用 db_query 按 id 读
func refFields(row map[string]any) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v, ok := row[k].(string)
		if !ok || strings.TrimSpace(v) == "" || k == "id" || strings.HasSuffix(k, "_at") || strings.HasSuffix(k, "_id") {
			continue
		}
		n := 300
		if k == "body" || k == "draft" || k == "text" {
			n = 2000
		}
		out = append(out, fmt.Sprintf("  %s: %s", k, clip(oneLine(htmlTagRe.ReplaceAllString(v, " ")), n)))
		if len(out) >= 12 {
			break
		}
	}
	return strings.Join(out, "\n")
}

func (s *Server) apiRefsResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Refs []string `json:"refs"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(in.Refs) > 50 {
		in.Refs = in.Refs[:50]
	}
	out := make([]refInfo, 0, len(in.Refs))
	for _, ref := range in.Refs {
		out = append(out, s.resolveRef(r.Context(), ref))
	}
	writeJSON(w, map[string]any{"list": out})
}
