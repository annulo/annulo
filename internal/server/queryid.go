package server

import (
	"encoding/json"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// id 是记录的系统主键，不是 JSON body 字段。已知主键时用 record_get，
// 再检查该行的其余条件；不全表扫描，也不把系统 id 发成 body.id 条件。
func queryRecordID(q localfn.Query) (id string, found, conflict bool, err error) {
	set := func(value any) {
		s, ok := value.(string)
		if !ok {
			err = i18n.New("记录 id 必须是字符串", "Record id must be a string")
			return
		}
		if found && id != s {
			conflict = true
		}
		id, found = s, true
	}
	for field, value := range q.Where {
		if strings.TrimPrefix(field, "body.") == "id" {
			set(value)
		}
	}
	hasID := false
	for _, c := range q.Filter {
		if c.Field == "id" {
			hasID = true
			if c.Op == "eq" {
				set(c.Value)
			}
		}
	}
	if hasID && !found && err == nil {
		err = i18n.New("按记录 id 查询请用 where: { id: \"…\" } 或 filter 的 id eq；按 id 翻页请用 cursor", "Query record ids using where: { id: \"…\" } or an id eq filter; use cursor for id pagination")
	}
	return
}

func matchesQuery(row map[string]any, q localfn.Query) bool {
	for field, value := range q.Where {
		if !same(row[strings.TrimPrefix(field, "body.")], value) {
			return false
		}
	}
	for _, c := range q.Filter {
		a := row[c.Field]
		switch c.Op {
		case "eq":
			if !same(a, c.Value) {
				return false
			}
		case "neq":
			if same(a, c.Value) {
				return false
			}
		case "in":
			values, ok := c.Value.([]any)
			if !ok {
				return false
			}
			matched := false
			for _, v := range values {
				if same(a, v) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		case "between":
			values, ok := c.Value.([]any)
			if !ok || len(values) != 2 {
				return false
			}
			lo, ok1 := compareQueryValue(a, values[0])
			hi, ok2 := compareQueryValue(a, values[1])
			if !ok1 || !ok2 || lo < 0 || hi > 0 {
				return false
			}
		default:
			n, ok := compareQueryValue(a, c.Value)
			if !ok {
				return false
			}
			if c.Op == "gt" && n <= 0 || c.Op == "gte" && n < 0 || c.Op == "lt" && n >= 0 || c.Op == "lte" && n > 0 {
				return false
			}
		}
	}
	return true
}

func compareQueryValue(a, b any) (int, bool) {
	normalize := func(v any) any {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out any
		json.Unmarshal(raw, &out)
		return out
	}
	a, b = normalize(a), normalize(b)
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		if x < y {
			return -1, true
		}
		if x > y {
			return 1, true
		}
		return 0, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(x, y), true
	}
	return 0, false
}
