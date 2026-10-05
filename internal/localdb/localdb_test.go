package localdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/creght"
)

func TestLocalDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(path) })
	rows := []map[string]any{
		{"title": "a", "views": 10, "tags": []any{"x", "y"}, "date": "2026-09-28", "status": "draft"},
		{"title": "b", "views": 5, "tags": []any{"y"}, "date": "2026-09-29", "status": "published"},
		{"title": "c", "views": "7", "date": "2026-10-01", "status": "published"},
	}
	var ids []string
	for _, r := range rows {
		id, err := d.CreateRecord(ctx, "", "posts", r)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	q := func(rq creght.RecordQuery) []string {
		list, _, _, err := d.QueryRecords(ctx, "", "posts", rq)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range list {
			var m map[string]any
			json.Unmarshal(r.Body, &m)
			out = append(out, m["title"].(string))
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
	eq(q(creght.RecordQuery{}), "c", "b", "a")
	eq(q(creght.RecordQuery{Where: map[string]any{"tags": "y"}, OrderBy: "views desc"}), "a", "b")
	eq(q(creght.RecordQuery{Filter: &creght.RecordFilter{Conditions: []creght.RecordCondition{{FieldID: "views", Operator: ">=", Value: 6}}}}), "a")
	eq(q(creght.RecordQuery{Filter: &creght.RecordFilter{Conditions: []creght.RecordCondition{{FieldID: "date", Operator: "between", Value: []any{"2026-09-29", "2026-10-01"}}}}, OrderBy: "date asc"}), "b", "c")
	eq(q(creght.RecordQuery{Filter: &creght.RecordFilter{Conditions: []creght.RecordCondition{{FieldID: "status", Operator: "neq", Value: "draft"}}}, Limit: 1, Offset: 1}), "b")
	list, next, _, _ := d.QueryRecords(ctx, "", "posts", creght.RecordQuery{OrderBy: "id asc", Limit: 2})
	if len(list) != 2 || next != ids[1] {
		t.Fatal(next, ids)
	}
	eq(q(creght.RecordQuery{OrderBy: "id asc", Cursor: next, Limit: 2}), "c")

	agg, _, err := d.AggregateRecords(ctx, "", "posts", map[string]any{"group_by": []any{"status"}, "metrics": []any{map[string]any{"op": "count"}, map[string]any{"op": "sum", "field": "views"}}, "order_by": "count desc"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(agg)
	if string(b) != `[{"count":2,"status":"published","sum_views":5},{"count":1,"status":"draft","sum_views":10}]` {
		t.Fatal(string(b))
	}
	agg, _, _ = d.AggregateRecords(ctx, "", "posts", map[string]any{"group_by": []any{map[string]any{"field": "date", "trunc": "month", "as": "m"}}, "metrics": []any{map[string]any{"op": "last", "field": "title", "order_by": "date"}}})
	b, _ = json.Marshal(agg)
	if string(b) != `[{"last_title":"b","m":"2026-09-01"},{"last_title":"c","m":"2026-10-01"}]` {
		t.Fatal(string(b))
	}
	if err := d.UpdateRecord(ctx, "", "posts", ids[0], map[string]any{"title": "a2"}); err != nil {
		t.Fatal(err)
	}
	r, _ := d.GetRecord(ctx, "", "posts", ids[0])
	if string(r.Body) != `{"title":"a2"}` {
		t.Fatal(string(r.Body))
	}
	d.DeleteRecord(ctx, "", "posts", ids[1])
	eq(q(creght.RecordQuery{}), "c", "a2")
	r, _ = d.GetRecord(ctx, "", "posts", "nope")
	if r.ID != "" {
		t.Fatal(r)
	}
}
