package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
)

// 运营后台的业务表（都在运营后台自己的 creght 项目里）：Shuttle 用 creght 登录态调平台接口读写。
// 只开放业务表：内置的 + 运营后台 tables/ 声明的（见 schema.go）。

type tableIDs struct {
	mu  sync.Mutex
	ids map[string]string // 项目 id + "/" + 表 key → 表 id
}

func (s *Server) tableID(ctx context.Context, key string) (string, error) {
	if !s.ready.Load() {
		return "", i18n.New("还没有初始化运营后台", "The back office isn't initialized yet")
	}
	return s.cur().tableID(ctx, key)
}

// tableStore 是业务表存在哪：creght 项目的表（*creght.Client），或者离线项目本机的 SQLite（*localdb.DB），两者方法一样。
type tableStore interface {
	TableID(ctx context.Context, projectID, key string) (string, error)
	QueryRecords(ctx context.Context, projectID, tableID string, q creght.RecordQuery) ([]creght.Record, string, int64, error)
	ListRecords(ctx context.Context, projectID, tableID string, q creght.RecordQuery) ([]creght.Record, error)
	CreateRecord(ctx context.Context, projectID, tableID string, body any) (string, error)
	GetRecord(ctx context.Context, projectID, tableID, recordID string) (*creght.Record, error)
	UpdateRecord(ctx context.Context, projectID, tableID, recordID string, body any) error
	DeleteRecord(ctx context.Context, projectID, tableID, recordID string) error
	AggregateRecords(ctx context.Context, projectID, tableID string, req map[string]any) ([]map[string]any, bool, error)
}

// projectDB 是一个运营后台项目的业务表读写：当前打开的项目一个（s.cur()），手机上调到别的项目时各建一个（relay.go）。
type projectDB struct {
	c         tableStore
	projectID string
	tids      *tableIDs
	isTable   func(key string) bool // 这个项目 tables/ 声明了这张表没有
	tableErr  func(fallback error) error
	ensure    func(ctx context.Context) error // 补齐声明了、平台上还没有的表；当前打开的在线项目才有
	onWrite   func(ctx context.Context, table string) // 写成功了：告诉打开着的页面这张表变了（datachanged.go）
}

func (p *projectDB) wrote(ctx context.Context, table string) {
	if p.onWrite != nil {
		p.onWrite(ctx, table)
	}
}

func (p *projectDB) tableID(ctx context.Context, key string) (string, error) {
	id, err := p.lookupTableID(ctx, key)
	if err != nil && p.ensure != nil && p.isTable(key) {
		// 刚建的项目：页面可能比补表先到（补表要一两秒），等补表跑完再找一次。
		// 要在 lookupTableID 外面等：EnsureTables 清缓存时也要拿 tids 的锁
		if eerr := p.ensure(ctx); eerr != nil {
			return "", err
		}
		id, err = p.lookupTableID(ctx, key)
	}
	return id, err
}

func (p *projectDB) lookupTableID(ctx context.Context, key string) (string, error) {
	p.tids.mu.Lock()
	defer p.tids.mu.Unlock()
	// 缓存按项目分开：切项目前发出的请求可能在清空缓存之后才查到旧项目的表 id，只按 key 存会让新项目一直用旧项目的表
	ck := p.projectID + "/" + key
	if id := p.tids.ids[ck]; id != "" {
		return id, nil
	}
	id, err := p.c.TableID(ctx, p.projectID, key)
	if err != nil {
		return "", err
	}
	if p.tids.ids == nil {
		p.tids.ids = map[string]string{}
	}
	p.tids.ids[ck] = id
	return id, nil
}

// cur 是当前打开的项目的 projectDB。
func (s *Server) cur() *projectDB {
	if s.ws.Offline {
		// 业务表在 Annulo 这边（SQLite / Postgres）。启动时没打开（数据库还没起来），用到时再试；还是不行就明确报错，不落到 creght 上
		s.dataMu.Lock()
		if s.localData == nil {
			db, err := s.openLocalData(s.ws.ProjectID)
			if err != nil {
				s.dataMu.Unlock()
				return &projectDB{c: errStore{i18n.Errorf("项目的数据库连不上：%v", "Can't reach the project's database: %v", err)}, projectID: s.ws.ProjectID, tids: &s.tids, isTable: s.isOpsTable, tableErr: s.tableErr, onWrite: s.dataChanged}
			}
			s.localData = db
		}
		db := s.localData
		s.dataMu.Unlock()
		return &projectDB{c: db, projectID: s.ws.ProjectID, tids: &s.tids, isTable: s.isOpsTable, tableErr: s.tableErr, onWrite: s.dataChanged}
	}
	return &projectDB{c: s.creght, projectID: s.ws.ProjectID, tids: &s.tids, isTable: s.isOpsTable, tableErr: s.tableErr, ensure: s.EnsureTables, onWrite: s.dataChanged}
}

type row = map[string]any

// dbList 读一张表的全部记录（按 id 游标读完，不受单页 1000 条的限制），每条带上 id；按创建时间倒序。
func (s *Server) dbList(ctx context.Context, table string) ([]row, error) {
	return s.dbQuery(ctx, table, creght.RecordQuery{})
}

// dbQuery 按条件读：过滤在服务端做。q.Limit 为 0 时读完满足条件的全部行（游标翻页），否则只读一页（最多 1000）。
// 没指定排序时按创建时间倒序（新的在前），和 dbList 一样。
func (s *Server) dbQuery(ctx context.Context, table string, q creght.RecordQuery) ([]row, error) {
	if q.Limit != 0 {
		q.SkipTotal = true
		out, _, _, err := s.dbQueryPage(ctx, table, q)
		return out, err
	}
	if !s.ready.Load() {
		return nil, i18n.New("还没有初始化运营后台", "The back office isn't initialized yet")
	}
	p := s.cur() // 表 id 和读记录用同一个项目，中途切了项目也不会拿 A 项目的表 id 去读 B 项目
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return nil, err
	}
	recs, err := p.c.ListRecords(ctx, p.projectID, tid, q)
	if err != nil {
		return nil, err
	}
	return sortedRows(recs, q), nil
}

// dbQueryPage 只读一页（最多 1000 条），next 是下一页的游标：q.OrderBy 是 "id asc" 时把它传回 q.Cursor 往后读，读完了是空。
// total 是满足条件的总行数；不需要就设 q.SkipTotal（省一次 COUNT），这时是 -1。
func (s *Server) dbQueryPage(ctx context.Context, table string, q creght.RecordQuery) ([]row, string, int64, error) {
	if !s.ready.Load() {
		return nil, "", 0, i18n.New("还没有初始化运营后台", "The back office isn't initialized yet")
	}
	return s.cur().queryPage(ctx, table, q)
}

func (p *projectDB) queryPage(ctx context.Context, table string, q creght.RecordQuery) ([]row, string, int64, error) {
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return nil, "", 0, err
	}
	recs, next, total, err := p.c.QueryRecords(ctx, p.projectID, tid, q)
	if err != nil {
		return nil, "", 0, err
	}
	if len(recs) < q.Limit {
		next = "" // 不满一页就是最后一页，别让调用方再多请求一次空页
	}
	return sortedRows(recs, q), next, total, nil
}

func sortedRows(recs []creght.Record, q creght.RecordQuery) []row {
	out := rowsOf(recs)
	if q.OrderBy == "" {
		// 新的在前。按时间比，不按字符串比：平台补的时间带 +08:00，本机函数写的是 Z，字符串顺序不对
		sort.SliceStable(out, func(i, j int) bool { return rowTime(out[i]).After(rowTime(out[j])) })
	}
	return out
}

// rowsOf 把平台的记录转成业务行：body 展开，带上 id；agent 用 CLI 直接写的记录可能没带时间，用平台记录的时间补上。
func rowsOf(recs []creght.Record) []row {
	out := make([]row, 0, len(recs))
	for _, r := range recs {
		var m row
		if json.Unmarshal(r.Body, &m) != nil || m == nil {
			continue
		}
		m["id"] = r.ID
		if str(m["created_at"]) == "" && r.CreatedAt != "" {
			m["created_at"] = r.CreatedAt
		}
		if str(m["updated_at"]) == "" && r.UpdatedAt != "" {
			m["updated_at"] = r.UpdatedAt
		}
		out = append(out, m)
	}
	return out
}

// rowTime 是记录的创建时间，解析不了的算最早。
func rowTime(m row) time.Time {
	t, err := time.Parse(time.RFC3339Nano, str(m["created_at"]))
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *Server) dbCreate(ctx context.Context, table string, m row) (row, error) {
	return s.cur().create(ctx, table, m)
}

// dbPatch 浅合并：patch 里给出的字段覆盖，值为 null 的字段清空（存成 null）。
func (s *Server) dbPatch(ctx context.Context, table, id string, patch row) (row, error) {
	return s.cur().patch(ctx, table, id, patch)
}

func (s *Server) dbDelete(ctx context.Context, table, id string) error {
	return s.cur().delete(ctx, table, id)
}

func (p *projectDB) create(ctx context.Context, table string, m row) (row, error) {
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return nil, err
	}
	delete(m, "id")
	now := time.Now().UTC().Format(time.RFC3339)
	if str(m["created_at"]) == "" {
		m["created_at"] = now
	}
	m["updated_at"] = now
	id, err := p.c.CreateRecord(ctx, p.projectID, tid, m)
	if err != nil {
		return nil, err
	}
	m["id"] = id
	p.wrote(ctx, table)
	return m, nil
}

// patch 浅合并：patch 里给出的字段覆盖，值为 null 的字段清空（存成 null）。
func (p *projectDB) patch(ctx context.Context, table, id string, patch row) (row, error) {
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return nil, err
	}
	rec, err := p.c.GetRecord(ctx, p.projectID, tid, id)
	if err != nil {
		return nil, err
	}
	var m row
	if err := json.Unmarshal(rec.Body, &m); err != nil || m == nil {
		m = row{}
	}
	for k, v := range patch {
		if k == "id" || k == "created_at" {
			continue
		}
		// 删字段要显式传 null：平台的更新是合并，body 里不带这个键它会保留旧值
		m[k] = v
	}
	m["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := p.c.UpdateRecord(ctx, p.projectID, tid, id, m); err != nil {
		return nil, err
	}
	m["id"] = id
	p.wrote(ctx, table)
	return m, nil
}

func (p *projectDB) delete(ctx context.Context, table, id string) error {
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return err
	}
	if err := p.c.DeleteRecord(ctx, p.projectID, tid, id); err != nil {
		return err
	}
	p.wrote(ctx, table)
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// apiDB：/_shuttle/api/db/<table>
//
//	GET                    列表
//	POST   {…}             新建
//	PATCH  ?id=… {…}       合并更新
//	DELETE ?id=…           删除
func (s *Server) apiDB(w http.ResponseWriter, r *http.Request, table string) {
	if !s.isOpsTable(table) {
		fail(w, http.StatusNotFound, s.tableErr(i18n.Errorf("没有这张表：%s", "No such table: %s", table)))
		return
	}
	ctx := byPage(r.Context()) // 页面自己写的：页面那边已经知道了（datachanged.go）
	readBody := func() (row, error) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		var m row
		if err := json.Unmarshal(b, &m); err != nil || m == nil {
			return nil, i18n.New("请求体要是 JSON 对象", "The request body must be a JSON object")
		}
		return m, nil
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.dbList(ctx, table)
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"list": list})
	case http.MethodPost:
		m, err := readBody()
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		out, err := s.dbCreate(ctx, table, m)
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, out)
	case http.MethodPatch:
		m, err := readBody()
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		out, err := s.dbPatch(ctx, table, r.URL.Query().Get("id"), m)
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		if err := s.dbDelete(ctx, table, r.URL.Query().Get("id")); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusMethodNotAllowed, i18n.New("不支持的方法", "Method not supported"))
	}
}

// errStore：业务表的存储打不开时，每个操作都报同一个错（不落到别的存储上）。
type errStore struct{ err error }

func (e errStore) TableID(context.Context, string, string) (string, error) { return "", e.err }
func (e errStore) QueryRecords(context.Context, string, string, creght.RecordQuery) ([]creght.Record, string, int64, error) {
	return nil, "", 0, e.err
}
func (e errStore) ListRecords(context.Context, string, string, creght.RecordQuery) ([]creght.Record, error) {
	return nil, e.err
}
func (e errStore) CreateRecord(context.Context, string, string, any) (string, error) {
	return "", e.err
}
func (e errStore) GetRecord(context.Context, string, string, string) (*creght.Record, error) {
	return nil, e.err
}
func (e errStore) UpdateRecord(context.Context, string, string, string, any) error { return e.err }
func (e errStore) DeleteRecord(context.Context, string, string, string) error      { return e.err }
func (e errStore) AggregateRecords(context.Context, string, string, map[string]any) ([]map[string]any, bool, error) {
	return nil, false, e.err
}
