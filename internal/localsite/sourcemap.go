package localsite

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 浏览器的 error.stack 里是编译产物 /_client/m/<文件>?v=… 的行列，sourcemap 只在 DevTools 里生效。
// 这里用 esbuild 出的 sourcemap 把堆栈换回源码位置（pages/Index.tsx:12:5），页面的报错框和给助手的 page_errors 都用它。

type sourceMap struct {
	sources []string
	lines   [][]mapping // 按生成代码的行（从 0 开始），每行按列升序
}

type mapping struct {
	genCol, src, srcLine, srcCol int
}

func parseSourceMap(b []byte) (*sourceMap, error) {
	var raw struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	m := &sourceMap{sources: raw.Sources}
	src, srcLine, srcCol := 0, 0, 0
	for _, line := range strings.Split(raw.Mappings, ";") {
		var segs []mapping
		genCol := 0
		for _, seg := range strings.Split(line, ",") {
			if seg == "" {
				continue
			}
			v := decodeVLQ(seg)
			if len(v) == 0 {
				continue
			}
			genCol += v[0]
			if len(v) < 4 {
				continue // 没有对应源码的片段
			}
			src += v[1]
			srcLine += v[2]
			srcCol += v[3]
			segs = append(segs, mapping{genCol, src, srcLine, srcCol})
		}
		m.lines = append(m.lines, segs)
	}
	return m, nil
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func decodeVLQ(s string) []int {
	var out []int
	val, shift := 0, 0
	for i := 0; i < len(s); i++ {
		d := strings.IndexByte(b64, s[i])
		if d < 0 {
			return nil
		}
		val += (d & 31) << shift
		if d&32 != 0 {
			shift += 5
			continue
		}
		if val&1 != 0 {
			out = append(out, -(val >> 1))
		} else {
			out = append(out, val>>1)
		}
		val, shift = 0, 0
	}
	return out
}

// lookup：line、col 是堆栈里的（从 1 开始），返回源码文件和行列（从 1 开始）。
func (m *sourceMap) lookup(line, col int) (file string, srcLine, srcCol int, ok bool) {
	if line < 1 || line > len(m.lines) {
		return "", 0, 0, false
	}
	segs := m.lines[line-1]
	i := sort.Search(len(segs), func(i int) bool { return segs[i].genCol > col-1 }) - 1
	if i < 0 {
		return "", 0, 0, false
	}
	s := segs[i]
	if s.src < 0 || s.src >= len(m.sources) {
		return "", 0, 0, false
	}
	return m.sources[s.src], s.srcLine + 1, s.srcCol + 1, true
}

var stackFrameRE = regexp.MustCompile(`(?:https?://[^/\s()]+)?/_client/m/([^?\s()]+)\?v=([0-9a-f]+):(\d+):(\d+)`)

// Symbolicate 把文本（堆栈、报错信息）里指向本地编译产物的位置换成源码位置；认不出的原样留着。
func (r *Renderer) Symbolicate(s string) string {
	if !strings.Contains(s, "/_client/m/") {
		return s
	}
	return stackFrameRE.ReplaceAllStringFunc(s, func(frame string) string {
		p := stackFrameRE.FindStringSubmatch(frame)
		m := r.sourceMap(p[1], p[2])
		if m == nil {
			return frame
		}
		line, _ := strconv.Atoi(p[3])
		col, _ := strconv.Atoi(p[4])
		file, l, c, ok := m.lookup(line, col)
		if !ok {
			return frame
		}
		return file + ":" + strconv.Itoa(l) + ":" + strconv.Itoa(c)
	})
}

func (r *Renderer) sourceMap(rel, ver string) *sourceMap {
	key := rel + "@" + ver
	r.mapsMu.Lock()
	m := r.maps[key]
	r.mapsMu.Unlock()
	if m != nil {
		return m
	}
	sv, ok := r.lookup(rel, ver)
	if !ok || sv.sourcemap == "" {
		return nil
	}
	m, err := parseSourceMap([]byte(sv.sourcemap))
	if err != nil {
		return nil
	}
	r.mapsMu.Lock()
	if r.maps == nil || len(r.maps) > 200 {
		r.maps = map[string]*sourceMap{}
	}
	r.maps[key] = m
	r.mapsMu.Unlock()
	return m
}
