package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// 按表里的时间字段到点跑（schedules/<id>.json 里写 due，不写 every / at）：
//
//	{ "name": "发布到点的帖子", "fn": "social/social.publish", "input": { "post_id": "$id" },
//	  "due": { "table": "social_posts", "field": "scheduled_at", "where": { "status": "scheduled" } } }
//
// 表里 where（字段相等）、filter（其余条件，写法同 ctx.db.query）都满足、field 有值且时间到了的行，每行跑一次 fn。
// input 里的 "$id" 换成这一行的 id，"$row.<字段>" 换成这一行那个字段的值；不写 input 是 { "id": "<行 id>" }。
//
// 排期存在业务行上，Annulo 不另存一份：改时间就是改字段，取消就是清空字段或改掉 where 里的状态。
// 同一行的同一个时间只跑一次（开始跑之前记下，不管成败都不重跑，失败由函数记在行上）；改了时间会再跑。
// Annulo 没开时错过的，下次打开补跑。同一条声明的行一条条跑（发布常要用本机浏览器）。
// 每 dueEvery 查一次表；声明了 due 的任务在设置 → 定时任务里列出接下来要跑的行。

// dueEvery：多久查一次表里有没有到点的行
const dueEvery = time.Minute

// dueRowTimeout：跑一行最长多久（和页面按钮跑本机函数一样）
const dueRowTimeout = localRunTimeout

// dueUpcomingMax：设置页里列出接下来要跑的最多几行
const dueUpcomingMax = 20

type dueDef struct {
	Table  string         `json:"table"`
	Field  string         `json:"field"`
	Where  map[string]any `json:"where,omitempty"`
	Filter any            `json:"filter,omitempty"`

	filter []localfn.Cond
}

// dueRow 是一行排了期的：设置页显示，也用来算下一次
type dueRow struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

func (d *dueDef) parse(rel string) error {
	if d.Table == "" || d.Field == "" {
		return i18n.Errorf("%s：due 要写 table（表）和 field（时间字段）", "%s: due needs table and field (the time field)", rel)
	}
	f, err := localfn.ParseConds(d.Filter)
	if err != nil {
		return i18n.Errorf("%s：due.filter 不对：%w", "%s: due.filter is invalid: %w", rel, err)
	}
	d.filter = f
	return nil
}

// dueTime 认 ISO8601（带不带时区都行，不带按本机时区）和毫秒时间戳；认不出来返回零值
func dueTime(v any) time.Time {
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(x)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if t, err := time.ParseInLocation(layout, x, time.Local); err == nil {
				return t
			}
		}
	case float64:
		if x > 0 {
			return time.UnixMilli(int64(x))
		}
	case int64:
		if x > 0 {
			return time.UnixMilli(x)
		}
	}
	return time.Time{}
}

// dueInput 把 input 里的 "$id"、"$row.<字段>" 换成这一行的值
func dueInput(in any, row map[string]any) any {
	id, _ := row["id"].(string)
	if in == nil {
		return map[string]any{"id": id}
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if x == "$id" {
				return id
			}
			if f, ok := strings.CutPrefix(x, "$row."); ok {
				return row[f]
			}
			return x
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, vv := range x {
				out[k] = walk(vv)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, vv := range x {
				out[i] = walk(vv)
			}
			return out
		}
		return v
	}
	return walk(in)
}

// dueStamp 是「这一行的这个时间」：记进 Done，同一个时间只跑一次
func dueStamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// runDue 查一次表，把到点的行一条条跑掉。返回跑了几行（0 表示这次没有到点的，不记运行记录）和结果摘要。
func (s *Server) runDue(sp schedProject, d scheduleDef, start time.Time) (ran int, summary string, err error) {
	host, err := s.projectHost(sp.ID)
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	q := localfn.Query{Where: d.Due.Where, Filter: append(append([]localfn.Cond{}, d.Due.filter...), localfn.Cond{Field: d.Due.Field, Op: "gt", Value: ""}), OrderBy: d.Due.Field + " asc", Limit: 500}
	list, _, _, err := host.DB.Query(ctx, d.Due.Table, q)
	cancel()
	if err != nil {
		return 0, "", i18n.Errorf("读表 %s 失败：%w", "Couldn't read table %s: %w", d.Due.Table, err)
	}

	key := d.key()
	sc := &s.sched
	var rows []dueRow
	byID := map[string]map[string]any{}
	for _, r := range list {
		id, _ := r["id"].(string)
		if t := dueTime(r[d.Due.Field]); id != "" && !t.IsZero() {
			rows = append(rows, dueRow{id, t})
			byID[id] = r
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })

	// 记下接下来要跑的；Done 里只留还在表里的行（跑完离开 where 的就忘掉）
	sc.mu.Lock()
	p := s.proj(sp.ID, sp.Dir)
	st := p.state[key]
	if st == nil {
		st = &scheduleState{}
		p.state[key] = st
	}
	done := map[string]string{}
	var todo, upcoming []dueRow
	for _, r := range rows {
		if v, ok := st.Done[r.ID]; ok {
			done[r.ID] = v
			if v == dueStamp(r.At) {
				continue
			}
		}
		if r.At.After(start) {
			upcoming = append(upcoming, r)
		} else {
			todo = append(todo, r)
		}
	}
	changed := len(done) != len(st.Done)
	st.Done = done
	p.upcoming[key] = upcoming
	if changed {
		p.save()
	}
	sc.mu.Unlock()

	var fails []string
	for _, r := range todo {
		// 开始跑之前记下：跑到一半 Annulo 退了，下次打开不会再发一遍
		sc.mu.Lock()
		if st.Done == nil {
			st.Done = map[string]string{}
		}
		st.Done[r.ID] = dueStamp(r.At)
		p.save()
		sc.mu.Unlock()

		h := host
		h.RunID = fmt.Sprintf("s%d", time.Now().UnixNano())
		rctx, rcancel := context.WithTimeout(context.Background(), dueRowTimeout)
		_, rerr := s.runFn(rctx, h, d.Fn, dueInput(d.Input, byID[r.ID]), func(localfn.Event) {})
		rcancel()
		ran++
		if rerr != nil {
			fails = append(fails, r.ID+": "+rerr.Error())
		}
	}
	if ran == 0 {
		return 0, "", nil
	}
	b, _ := json.Marshal(map[string]any{"ran": ran, "failed": len(fails)})
	if len(fails) > 0 {
		return ran, string(b), i18n.Errorf("%d 行里 %d 行失败：%s", "%[2]d of %[1]d rows failed: %[3]s", ran, len(fails), strings.Join(fails, i18n.T("；", "; ")))
	}
	return ran, string(b), nil
}
