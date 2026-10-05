package server

import (
	"testing"

	"github.com/annulo/annulo/internal/localfn"
)

func TestDBToolsSchema(t *testing.T) {
	tools := (&Server{}).dbTools() // schema 写错会 panic
	if len(tools) != 2 || tools[0].Parameters.Type != "object" {
		t.Fatalf("tools: %+v", tools)
	}
	if _, err := (&Server{}).dbToolQuery(t.Context(), map[string]any{"table": "x"}); err == nil {
		t.Fatal("没有项目时应该报错")
	}
}

func TestParseConds(t *testing.T) {
	cs, err := localfn.ParseConds([]any{map[string]any{"field": "body.views", "op": "gte", "value": 10.0}})
	if err != nil || len(cs) != 1 || cs[0].Field != "views" {
		t.Fatalf("%v %+v", err, cs)
	}
	// 站点 Func 的写法（平台的键名）也认，见 localfn.ParseConds
	for _, ok := range []any{
		[]any{map[string]any{"fieldId": "views", "operator": "gte", "value": 1}},
		map[string]any{"conditions": []any{}},
	} {
		if _, err := localfn.ParseConds(ok); err != nil {
			t.Errorf("站点 Func 的写法应该能用：%v %v", ok, err)
		}
	}
	for _, bad := range []any{
		[]any{map[string]any{"field": "views", "op": "after", "value": 1}},
		[]any{map[string]any{"field": "views", "op": "eq", "value": 1, "extra": 1}},
		map[string]any{"match": "or", "conditions": []any{}},
	} {
		if _, err := localfn.ParseConds(bad); err == nil {
			t.Errorf("应该报错：%v", bad)
		}
	}
}
