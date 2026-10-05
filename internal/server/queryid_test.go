package server

import (
	"github.com/annulo/annulo/internal/localfn"
	"testing"
)

func TestRecordIDQuery(t *testing.T) {
	const id = "p9u9h50g7w5e"
	for _, q := range []localfn.Query{
		{Where: map[string]any{"id": id}},
		{Filter: []localfn.Cond{{Field: "id", Op: "eq", Value: id}}},
	} {
		got, found, conflict, err := queryRecordID(q)
		if err != nil || got != id || !found || conflict {
			t.Fatalf("%q %v %v %v", got, found, conflict, err)
		}
		row := map[string]any{"id": id, "title": "existing article", "views": float64(100), "date": "2026-09-28"}
		if !matchesQuery(row, q) {
			t.Fatal("system record id must match even without body.id")
		}
	}
	q := localfn.Query{Where: map[string]any{"id": id, "status": "draft"}, Filter: []localfn.Cond{{Field: "views", Op: "gte", Value: int64(100)}}}
	row := map[string]any{"id": id, "status": "draft", "views": float64(100)}
	if !matchesQuery(row, q) {
		t.Fatal("id lookup must apply remaining conditions")
	}
	row["status"] = "published"
	if matchesQuery(row, q) {
		t.Fatal("id lookup must not ignore status condition")
	}
	q = localfn.Query{Where: map[string]any{"id": id}, Filter: []localfn.Cond{{Field: "id", Op: "eq", Value: "different"}}}
	_, _, conflict, err := queryRecordID(q)
	if err != nil || !conflict {
		t.Fatal("conflicting ids must return no record")
	}
	if _, _, _, err := queryRecordID(localfn.Query{Filter: []localfn.Cond{{Field: "id", Op: "neq", Value: id}}}); err == nil {
		t.Fatal("unsupported system-id filters must not silently search body.id")
	}
	if same("100", float64(100)) {
		t.Fatal("JSON field types matter")
	}
}
