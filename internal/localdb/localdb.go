// Package localdb 是离线项目的业务表：存在本机的 SQLite 里，接口和语义照 creght 平台的项目表
// （record_list / record / record_aggregate）。
//
// 一个项目一个数据库文件（~/.shuttle/workspaces/<项目>/data.db，不在项目目录里：不进 git、不推）。
// 一张表的行全部读进内存过滤、排序、聚合：运营后台的表是几千行的量级，这样语义最好对齐，也不用把条件翻译成 SQL。
// 写入时更新内存里的那份。只有 Shuttle 进程自己读写这个文件。
package localdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/annulo/annulo/internal/creght"
)

type DB struct {
	db   *sql.DB
	mu   sync.Mutex
	rows map[string][]*row // 表 → 行（按 id 升序）；读过一次就缓存
}

type row struct {
	id        string
	body      map[string]any
	raw       json.RawMessage
	createdAt string
	updatedAt string
}

var (
	openMu sync.Mutex
	opened = map[string]*DB{}
)

// Close 关掉 Open 过的文件（测试收尾用：Windows 上开着的文件删不掉）。
func Close(path string) error {
	openMu.Lock()
	defer openMu.Unlock()
	d := opened[path]
	if d == nil {
		return nil
	}
	delete(opened, path)
	return d.db.Close()
}

// Open 打开（没有就建）一个数据库文件。同一个文件只打开一次。
func Open(path string) (*DB, error) {
	openMu.Lock()
	defer openMu.Unlock()
	if d := opened[path]; d != nil {
		return d, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS records (
		tbl TEXT NOT NULL, id TEXT NOT NULL, body TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (tbl, id))`); err != nil {
		db.Close()
		return nil, err
	}
	d := &DB{db: db, rows: map[string][]*row{}}
	opened[path] = d
	return d, nil
}

// load 取一张表的全部行（调用方持有 d.mu）。
func (d *DB) load(table string) ([]*row, error) {
	if rs, ok := d.rows[table]; ok {
		return rs, nil
	}
	q, err := d.db.Query(`SELECT id, body, created_at, updated_at FROM records WHERE tbl = ? ORDER BY id`, table)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	rs := []*row{}
	for q.Next() {
		r := &row{}
		var body string
		if err := q.Scan(&r.id, &body, &r.createdAt, &r.updatedAt); err != nil {
			return nil, err
		}
		r.raw = json.RawMessage(body)
		if json.Unmarshal(r.raw, &r.body) != nil || r.body == nil {
			r.body = map[string]any{}
		}
		rs = append(rs, r)
	}
	d.rows[table] = rs
	return rs, q.Err()
}

// snapshot 是一张表此刻的行（复制一份切片，读的时候不怕别人写）。
func (d *DB) snapshot(table string) ([]*row, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rs, err := d.load(table)
	return append([]*row(nil), rs...), err
}

var idMu sync.Mutex
var lastMicro int64

// newID 生成按时间递增的 id：游标翻页（id asc）和默认的新的在前都靠它的顺序。
func newID() string {
	idMu.Lock()
	t := time.Now().UnixMicro()
	if t <= lastMicro {
		t = lastMicro + 1
	}
	lastMicro = t
	idMu.Unlock()
	var b [2]byte
	rand.Read(b[:])
	return fmt.Sprintf("r%012x%04x", t, binary.BigEndian.Uint16(b[:]))
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Tables 是库里有数据的表。
func (d *DB) Tables() ([]string, error) {
	q, err := d.db.Query(`SELECT DISTINCT tbl FROM records ORDER BY tbl`)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []string
	for q.Next() {
		var t string
		q.Scan(&t)
		out = append(out, t)
	}
	return out, q.Err()
}

// ---- 和 creght.Client 同名同参数的方法：server 的 projectDB 换一个实现就能用 ----

// TableID：本地的表不用建，key 就是 id。
func (d *DB) TableID(_ context.Context, _ string, key string) (string, error) { return key, nil }

func record(r *row) creght.Record {
	return creght.Record{ID: r.id, Body: r.raw, CreatedAt: r.createdAt, UpdatedAt: r.updatedAt}
}

func (d *DB) CreateRecord(_ context.Context, _, table string, body any) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rs, err := d.load(table)
	if err != nil {
		return "", err
	}
	r := &row{id: newID(), raw: b, createdAt: now()}
	r.updatedAt = r.createdAt
	json.Unmarshal(b, &r.body)
	if r.body == nil {
		r.body = map[string]any{}
	}
	if _, err := d.db.Exec(`INSERT INTO records (tbl, id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, table, r.id, string(b), r.createdAt, r.updatedAt); err != nil {
		return "", err
	}
	d.rows[table] = append(rs, r)
	return r.id, nil
}

// ImportRecord 按给定的 id 和时间写一行（迁移用），已经有就覆盖。
func (d *DB) ImportRecord(table, id string, body json.RawMessage, createdAt, updatedAt string) error {
	if createdAt == "" {
		createdAt = now()
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT OR REPLACE INTO records (tbl, id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, table, id, string(body), createdAt, updatedAt)
	delete(d.rows, table)
	return err
}

func (d *DB) GetRecord(_ context.Context, _, table, id string) (*creght.Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rs, err := d.load(table)
	if err != nil {
		return nil, err
	}
	if i := find(rs, id); i >= 0 {
		r := record(rs[i])
		return &r, nil
	}
	return &creght.Record{}, nil // 和平台一样：不存在返回空记录
}

func find(rs []*row, id string) int {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].id >= id })
	if i < len(rs) && rs[i].id == id {
		return i
	}
	return -1
}

// UpdateRecord 整体替换 body。
func (d *DB) UpdateRecord(_ context.Context, _, table, id string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rs, err := d.load(table)
	if err != nil {
		return err
	}
	i := find(rs, id)
	if i < 0 {
		return fmt.Errorf("record not found: %s", id)
	}
	t := now()
	if _, err := d.db.Exec(`UPDATE records SET body = ?, updated_at = ? WHERE tbl = ? AND id = ?`, string(b), t, table, id); err != nil {
		return err
	}
	r := *rs[i]
	r.raw, r.updatedAt, r.body = b, t, nil
	json.Unmarshal(b, &r.body)
	if r.body == nil {
		r.body = map[string]any{}
	}
	rs[i] = &r
	return nil
}

func (d *DB) DeleteRecord(_ context.Context, _, table, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rs, err := d.load(table)
	if err != nil {
		return err
	}
	if _, err := d.db.Exec(`DELETE FROM records WHERE tbl = ? AND id = ?`, table, id); err != nil {
		return err
	}
	if i := find(rs, id); i >= 0 {
		d.rows[table] = append(rs[:i:i], rs[i+1:]...)
	}
	return nil
}

// QueryRecords 查一页：过滤、排序、offset / limit，OrderBy 是 "id asc" 时 Cursor 往后翻。
func (d *DB) QueryRecords(_ context.Context, _, table string, q creght.RecordQuery) ([]creght.Record, string, int64, error) {
	rs, err := d.snapshot(table)
	if err != nil {
		return nil, "", 0, err
	}
	m, err := newMatcher(q.Where, q.Filter)
	if err != nil {
		return nil, "", 0, err
	}
	var hit []*row
	for _, r := range rs {
		if m.match(r) {
			hit = append(hit, r)
		}
	}
	total := int64(len(hit))
	if q.SkipTotal {
		total = -1
	}
	order := strings.TrimSpace(q.OrderBy)
	if order == "" {
		order = "id desc"
	}
	cursorMode := strings.EqualFold(strings.Join(strings.Fields(order), " "), "id asc")
	if err := sortRows(hit, order); err != nil {
		return nil, "", 0, err
	}
	if cursorMode && q.Cursor != "" {
		i := sort.Search(len(hit), func(i int) bool { return hit[i].id > q.Cursor })
		hit = hit[i:]
	}
	if q.Offset > 0 {
		if q.Offset >= len(hit) {
			hit = nil
		} else {
			hit = hit[q.Offset:]
		}
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	next := ""
	if len(hit) > limit {
		hit = hit[:limit]
		if cursorMode {
			next = hit[len(hit)-1].id
		}
	}
	out := make([]creght.Record, len(hit))
	for i, r := range hit {
		out[i] = record(r)
	}
	return out, next, total, nil
}

// ListRecords 读完满足条件的全部行（id 升序）。
func (d *DB) ListRecords(ctx context.Context, pid, table string, q creght.RecordQuery) ([]creght.Record, error) {
	q.OrderBy, q.Limit, q.SkipTotal, q.Cursor = "id asc", 1000, true, ""
	var all []creght.Record
	for {
		list, next, _, err := d.QueryRecords(ctx, pid, table, q)
		if err != nil {
			return nil, err
		}
		all = append(all, list...)
		if next == "" {
			return all, nil
		}
		q.Cursor = next
	}
}

// ---- 取值、比较 ----

// field 取一行的字段：id、created_at / updated_at 是系统列（body 里没写时用它），其余是 body 里的（可以点分嵌套）。
func (r *row) field(name string) (any, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "body.")
	switch name {
	case "id":
		return r.id, true
	}
	var cur any = r.body
	for _, k := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			cur = nil
			break
		}
		if cur, ok = m[k]; !ok {
			cur = nil
			break
		}
	}
	if cur == nil {
		switch name {
		case "created_at":
			return r.createdAt, true
		case "updated_at":
			return r.updatedAt, true
		}
		return nil, false
	}
	return cur, true
}

// systemField：排序、分组、first / last 时 created_at / updated_at 用系统列（和平台一样）。
func (r *row) systemField(name string) (any, bool) {
	switch strings.TrimSpace(name) {
	case "created_at":
		return r.createdAt, true
	case "updated_at":
		return r.updatedAt, true
	}
	return r.field(name)
}

// norm 把值转成 JSON 解出来的样子（数字都是 float64），比较前先统一。
func norm(v any) any {
	switch x := v.(type) {
	case nil, string, bool, float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	json.Unmarshal(b, &out)
	return out
}

// contains 是 jsonb 的 @>：标量相等；数组包含（标量也能匹配数组里的一项）；对象是子集。
func contains(a, v any) bool {
	a, v = norm(a), norm(v)
	switch vv := v.(type) {
	case map[string]any:
		am, ok := a.(map[string]any)
		if !ok {
			return false
		}
		for k, x := range vv {
			if !contains(am[k], x) {
				return false
			}
		}
		return true
	case []any:
		aa, ok := a.([]any)
		if !ok {
			return false
		}
		for _, x := range vv {
			found := false
			for _, y := range aa {
				if contains(y, x) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	if aa, ok := a.([]any); ok {
		for _, y := range aa {
			if equalScalar(y, v) {
				return true
			}
		}
		return false
	}
	return equalScalar(a, v)
}

func equalScalar(a, b any) bool {
	if a == nil || b == nil {
		return false // jsonb 里 null @> null 为真，但字段缺失不算；平台上缺失的字段不匹配任何值
	}
	return a == b
}

// compare：数字按数值、字符串按字典序；类型不同返回 false（不参与比较）。
func compare(a, b any) (int, bool) {
	a, b = norm(a), norm(b)
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(x, y), true
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		switch {
		case x == y:
			return 0, true
		case !x:
			return -1, true
		}
		return 1, true
	}
	return 0, false
}

type cond struct {
	field string
	op    string
	value any
}

type matcher struct{ conds []cond }

func newMatcher(where map[string]any, f *creght.RecordFilter) (*matcher, error) {
	m := &matcher{}
	for k, v := range where {
		if strings.TrimSpace(k) == "" {
			continue
		}
		m.conds = append(m.conds, cond{k, "eq", v})
	}
	if f != nil {
		for i, c := range f.Conditions {
			if strings.TrimSpace(c.FieldID) == "" {
				return nil, fmt.Errorf("filter condition #%d has no fieldId", i+1)
			}
			op := c.Operator
			switch op {
			case "", "=", "equal", "eq":
				op = "eq"
			case "!=", "not_equal", "notEqual", "neq":
				op = "neq"
			case ">":
				op = "gt"
			case ">=":
				op = "gte"
			case "<":
				op = "lt"
			case "<=":
				op = "lte"
			case "in", "gt", "gte", "lt", "lte", "between":
			default:
				return nil, fmt.Errorf("invalid operator %q on %q", c.Operator, c.FieldID)
			}
			if op == "between" {
				vs, _ := norm(c.Value).([]any)
				if len(vs) != 2 {
					return nil, fmt.Errorf(`operator "between" on %q needs value [from, to] (both inclusive)`, c.FieldID)
				}
			}
			m.conds = append(m.conds, cond{c.FieldID, op, c.Value})
		}
	}
	return m, nil
}

func (m *matcher) match(r *row) bool {
	for _, c := range m.conds {
		a, _ := r.field(c.field)
		switch c.op {
		case "eq":
			if !contains(a, c.value) {
				return false
			}
		case "neq":
			if contains(a, c.value) {
				return false
			}
		case "in":
			vs, _ := norm(c.value).([]any)
			ok := false
			for _, v := range vs {
				if contains(a, v) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		case "between":
			vs, _ := norm(c.value).([]any)
			lo, ok1 := compare(a, vs[0])
			hi, ok2 := compare(a, vs[1])
			if !ok1 || !ok2 || lo < 0 || hi > 0 {
				return false
			}
		default:
			n, ok := compare(a, c.value)
			if !ok || c.op == "gt" && n <= 0 || c.op == "gte" && n < 0 || c.op == "lt" && n >= 0 || c.op == "lte" && n > 0 {
				return false
			}
		}
	}
	return true
}

// orderValue 比较两个排序值：缺失的排最后；类型不同按 数字 < 字符串 < 布尔 < 其他。
func orderValue(a, b any, aok, bok bool) int {
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return 1
	case !bok:
		return -1
	}
	if n, ok := compare(a, b); ok {
		return n
	}
	rank := func(v any) int {
		switch norm(v).(type) {
		case float64:
			return 0
		case string:
			return 1
		case bool:
			return 2
		}
		return 3
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra - rb
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return strings.Compare(string(x), string(y))
}

// sortRows 按 "字段 [asc|desc], …" 排序，同值按 id（和第一个排序方向一致）。缺失的值总在最后。
func sortRows(rs []*row, order string) error {
	type key struct {
		field string
		desc  bool
	}
	var keys []key
	for _, term := range strings.Split(order, ",") {
		f := strings.Fields(term)
		if len(f) == 0 {
			continue
		}
		if len(f) > 2 {
			return fmt.Errorf("invalid order_by %q", term)
		}
		k := key{field: f[0]}
		if len(f) == 2 {
			switch strings.ToLower(f[1]) {
			case "asc":
			case "desc":
				k.desc = true
			default:
				return fmt.Errorf("invalid order_by direction %q", f[1])
			}
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		keys = []key{{"id", true}}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		for _, k := range keys {
			a, aok := rs[i].systemField(k.field)
			b, bok := rs[j].systemField(k.field)
			n := orderValue(a, b, aok, bok)
			if n == 0 {
				continue
			}
			if !aok || !bok {
				return n < 0 // 缺失的不随方向翻转
			}
			if k.desc {
				return n > 0
			}
			return n < 0
		}
		if keys[0].desc {
			return rs[i].id > rs[j].id
		}
		return rs[i].id < rs[j].id
	})
	return nil
}
