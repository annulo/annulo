package inbox

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// 收到时的通知只做字段路径替换：{{payload.json.name}}、{{payload.headers.X-Event}}、{{source.id}}。
// 不跑代码，云端也能安全地执行；要判断、要带上别的数据的通知写在 handler 里。

var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

// Lookup 按 a.b.0.c 取值。
func Lookup(v any, path string) (any, bool) {
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			n, ok := x[k]
			if !ok {
				return nil, false
			}
			v = n
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// EventData 把事件变成普通的 JSON 值：按路径取值、交给 handler 都用它。
func EventData(e Event) any {
	b, _ := json.Marshal(e)
	var v any
	json.Unmarshal(b, &v)
	return v
}

func fill(s string, data any, extra map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		path := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := extra[path]; ok {
			return v
		}
		v, _ := Lookup(data, path)
		return stringify(v)
	})
}

// RenderNotify 算出一条通知要发的 JSON。
func RenderNotify(n NotifyDef, e Event) ([]byte, error) {
	data := EventData(e)
	text := fill(n.Text, data, nil)
	if len(n.Body) == 0 {
		return json.Marshal(map[string]string{"text": text})
	}
	var body any
	if err := json.Unmarshal(n.Body, &body); err != nil {
		return nil, err
	}
	extra := map[string]string{"text": text}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return fill(x, data, extra)
		case map[string]any:
			for k, c := range x {
				x[k] = walk(c)
			}
		case []any:
			for i, c := range x {
				x[i] = walk(c)
			}
		}
		return v
	}
	return json.Marshal(walk(body))
}
