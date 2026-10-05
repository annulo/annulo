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
	ids map[string]string
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
}

func (p *projectDB) tableID(ctx context.Context, key string) (string, error) {
	p.tids.mu.Lock()
	defer p.tids.mu.Unlock()
	if id := p.tids.ids[key]; id != "" {
		return id, nil
	}
	id, err := p.c.TableID(ctx, p.projectID, key)
	if err != nil {
		return "", err
	}
	if p.tids.ids == nil {
		p.tids.ids = map[string]string{}
	}
	p.tids.ids[key] = id
	return id, nil
}

// cur 是当前打开的项目的 projectDB。
func (s *Server) cur() *projectDB {
	if s.ws.Offline && s.localData != nil {
		return &projectDB{c: s.localData, projectID: s.ws.ProjectID, tids: &s.tids, isTable: s.isOpsTable, tableErr: s.tableErr}
	}
	return &projectDB{c: s.creght, projectID: s.ws.ProjectID, tids: &s.tids, isTable: s.isOpsTable, tableErr: s.tableErr}
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
	tid, err := s.tableID(ctx, table)
	if err != nil {
		return nil, err
	}
	recs, err := s.cur().c.ListRecords(ctx, s.ws.ProjectID, tid, q)
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
	return m, nil
}

func (p *projectDB) delete(ctx context.Context, table, id string) error {
	tid, err := p.tableID(ctx, table)
	if err != nil {
		return err
	}
	return p.c.DeleteRecord(ctx, p.projectID, tid, id)
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
	ctx := r.Context()
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
