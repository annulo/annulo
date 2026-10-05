package localdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/creght"
)

// AggregateRecords 是 record_aggregate：按字段（时间可截到 day / week / month / year）分组，
// 算 count / sum / avg / min / max / first / last。参数、输出的列名、排序和平台一致。
func (d *DB) AggregateRecords(_ context.Context, _, table string, req map[string]any) ([]map[string]any, bool, error) {
	var p struct {
		Where    map[string]any       `json:"where"`
		Filter   *creght.RecordFilter `json:"filter"`
		GroupBy  []json.RawMessage    `json:"group_by"`
		Metrics  []metric             `json:"metrics"`
		Timezone string               `json:"timezone"`
		OrderBy  string               `json:"order_by"`
		Limit    int                  `json:"limit"`
	}
	b, _ := json.Marshal(req)
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, false, fmt.Errorf("aggregate: %w", err)
	}
	tz := strings.TrimSpace(p.Timezone)
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, false, fmt.Errorf("invalid timezone %q (IANA name, e.g. Asia/Shanghai, UTC)", tz)
	}
	if len(p.Metrics) == 0 {
		return nil, false, fmt.Errorf(`metrics is required, e.g. [{"op":"count"}, {"op":"sum","field":"views"}]`)
	}
	var groups []groupBy
	for _, raw := range p.GroupBy {
		var g groupBy
		var s string
		if json.Unmarshal(raw, &s) == nil {
			g.Field = s
		} else if err := json.Unmarshal(raw, &g); err != nil {
			return nil, false, fmt.Errorf("invalid group_by: %s", raw)
		}
		g.Trunc = strings.ToLower(strings.TrimSpace(g.Trunc))
		switch g.Trunc {
		case "", "day", "week", "month", "year":
		default:
			return nil, false, fmt.Errorf("invalid trunc %q on %q: use day, week, month or year", g.Trunc, g.Field)
		}
		if g.As == "" {
			g.As = defaultAlias(g.Field)
		}
		groups = append(groups, g)
	}
	var aliases []string
	seen := map[string]bool{}
	add := func(a string) error {
		if seen[a] {
			return fmt.Errorf("duplicate output name %q: set a different \"as\"", a)
		}
		seen[a] = true
		aliases = append(aliases, a)
		return nil
	}
	for _, g := range groups {
		if err := add(g.As); err != nil {
			return nil, false, err
		}
	}
	for i := range p.Metrics {
		m := &p.Metrics[i]
		m.Op = strings.ToLower(strings.TrimSpace(m.Op))
		switch m.Op {
		case "count":
			if m.As == "" {
				m.As = "count"
			}
		case "sum", "avg", "min", "max", "first", "last":
			if strings.TrimSpace(m.Field) == "" {
				return nil, false, fmt.Errorf("metric %q needs a field", m.Op)
			}
			if m.As == "" {
				m.As = m.Op + "_" + defaultAlias(m.Field)
			}
		default:
			return nil, false, fmt.Errorf("invalid metric op %q: use count, sum, avg, min, max, first or last", m.Op)
		}
		if err := add(m.As); err != nil {
			return nil, false, err
		}
	}

	rs, err := d.snapshot(table)
	if err != nil {
		return nil, false, err
	}
	mt, err := newMatcher(p.Where, p.Filter)
	if err != nil {
		return nil, false, err
	}
	type bucket struct {
		keys []any
		rows []*row
	}
	var order []string
	buckets := map[string]*bucket{}
	for _, r := range rs {
		if !mt.match(r) {
			continue
		}
		keys := make([]any, len(groups))
		for i, g := range groups {
			v, ok := r.systemField(g.Field)
			if !ok {
				v = nil
			}
			if g.Trunc != "" {
				v = truncTime(v, g.Trunc, loc, g.Field == "created_at" || g.Field == "updated_at")
			}
			keys[i] = norm(v)
		}
		kb, _ := json.Marshal(keys)
		bk := buckets[string(kb)]
		if bk == nil {
			bk = &bucket{keys: keys}
			buckets[string(kb)] = bk
			order = append(order, string(kb))
		}
		bk.rows = append(bk.rows, r)
	}
	if len(groups) == 0 && len(buckets) == 0 {
		buckets["[]"] = &bucket{} // 没分组：整张表一组，没有行也返回一行（count 0）
		order = []string{"[]"}
	}
	out := make([]map[string]any, 0, len(buckets))
	for _, k := range order {
		bk := buckets[k]
		res := map[string]any{}
		for i, g := range groups {
			res[g.As] = bk.keys[i]
		}
		for _, m := range p.Metrics {
			res[m.As] = m.compute(bk.rows)
		}
		out = append(out, res)
	}

	// 排序：指定的输出列，不指定按分组列升序；空值排最后
	type ok struct {
		name string
		desc bool
	}
	var oks []ok
	if ob := strings.TrimSpace(p.OrderBy); ob != "" {
		for _, term := range strings.Split(ob, ",") {
			f := strings.Fields(term)
			if len(f) == 0 || len(f) > 2 {
				return nil, false, fmt.Errorf("invalid order_by %q: use \"<output name> [asc|desc]\"", term)
			}
			if !seen[f[0]] {
				return nil, false, fmt.Errorf("order_by %q is not an output name (have: %s)", f[0], strings.Join(aliases, ", "))
			}
			o := ok{name: f[0]}
			if len(f) == 2 {
				switch strings.ToLower(f[1]) {
				case "asc":
				case "desc":
					o.desc = true
				default:
					return nil, false, fmt.Errorf("invalid order_by direction %q", f[1])
				}
			}
			oks = append(oks, o)
		}
	} else {
		for _, g := range groups {
			oks = append(oks, ok{name: g.As})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		for _, o := range oks {
			a, b := out[i][o.name], out[j][o.name]
			n := orderValue(a, b, a != nil, b != nil)
			if n == 0 {
				continue
			}
			if a == nil || b == nil {
				return n < 0
			}
			if o.desc {
				return n > 0
			}
			return n < 0
		}
		return false
	})
	limit := p.Limit
	if limit <= 0 {
		limit = 1000
	}
	limit = min(limit, 10000)
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

type groupBy struct {
	Field string `json:"field"`
	Trunc string `json:"trunc"`
	As    string `json:"as"`
}

type metric struct {
	Op      string `json:"op"`
	Field   string `json:"field"`
	As      string `json:"as"`
	OrderBy string `json:"order_by"`
}

func defaultAlias(field string) string {
	field = strings.TrimPrefix(strings.TrimSpace(field), "body.")
	if i := strings.LastIndex(field, "."); i >= 0 {
		field = field[i+1:]
	}
	return field
}

func (m metric) compute(rows []*row) any {
	switch m.Op {
	case "count":
		return float64(len(rows))
	case "first", "last":
		ord := m.OrderBy
		if strings.TrimSpace(ord) == "" {
			ord = "created_at"
		}
		var best *row
		var bv any
		var bok bool
		for _, r := range rows {
			v, ok := r.systemField(ord)
			if best == nil {
				best, bv, bok = r, v, ok
				continue
			}
			n := orderValue(v, bv, ok, bok)
			if n == 0 {
				if m.Op == "first" && r.id < best.id || m.Op == "last" && r.id > best.id {
					best, bv, bok = r, v, ok
				}
				continue
			}
			if !ok || !bok {
				if n < 0 {
					best, bv, bok = r, v, ok
				}
				continue
			}
			if m.Op == "first" && n < 0 || m.Op == "last" && n > 0 {
				best, bv, bok = r, v, ok
			}
		}
		if best == nil {
			return nil
		}
		v, _ := best.systemField(m.Field)
		return norm(v)
	}
	// sum / avg / min / max：只算数字，字符串 "12"、缺失都不算
	var nums []float64
	for _, r := range rows {
		v, _ := r.field(m.Field)
		if f, ok := norm(v).(float64); ok {
			nums = append(nums, f)
		}
	}
	if len(nums) == 0 {
		return nil
	}
	switch m.Op {
	case "sum":
		var s float64
		for _, n := range nums {
			s += n
		}
		return s
	case "avg":
		var s float64
		for _, n := range nums {
			s += n
		}
		return math.Round(s/float64(len(nums))*1e6) / 1e6
	case "min":
		x := nums[0]
		for _, n := range nums {
			x = math.Min(x, n)
		}
		return x
	case "max":
		x := nums[0]
		for _, n := range nums {
			x = math.Max(x, n)
		}
		return x
	}
	return nil
}

var (
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	tsRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}`)
)

// truncTime 把时间截到 day / week（周一）/ month / year，输出 YYYY-MM-DD。YYYY-MM-DD 当作那个时区的日期不再换算；
// ISO 时间换算到时区再截；别的格式归到 null 组。
func truncTime(v any, unit string, loc *time.Location, column bool) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	var t time.Time
	switch {
	case dateRe.MatchString(s) && !column:
		t, _ = time.ParseInLocation("2006-01-02", s, loc)
	case tsRe.MatchString(s):
		var err error
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
			if t, err = time.Parse(layout, s); err == nil {
				break
			}
		}
		if err != nil {
			return nil
		}
		t = t.In(loc)
	default:
		return nil
	}
	y, mo, d := t.Date()
	switch unit {
	case "week":
		wd := (int(t.Weekday()) + 6) % 7 // 周一是 0
		t = time.Date(y, mo, d-wd, 0, 0, 0, 0, loc)
	case "month":
		t = time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	case "year":
		t = time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	default:
		t = time.Date(y, mo, d, 0, 0, 0, 0, loc)
	}
	return t.Format("2006-01-02")
}
