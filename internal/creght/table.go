package creght

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/annulo/annulo/internal/i18n"
)

// 项目 JSON 表（/api/p/project/:pid/table…）。运营后台的业务数据（渠道、内容、线索…）
// 都存在运营后台项目自己的表里，手机和同事打开线上后台也能读到。

type Record struct {
	ID        string          `json:"id"`
	Body      json.RawMessage `json:"body"`
	CreatedAt string          `json:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

// TableID 按 key 找表 id。
func (c *Client) TableID(ctx context.Context, projectID, key string) (string, error) {
	var ret struct {
		List []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"list"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/api/p/project/%s/table_list", url.PathEscape(projectID)), url.Values{"limit": {"200"}}, &ret); err != nil {
		return "", err
	}
	for _, t := range ret.List {
		if t.Key == key {
			return t.ID, nil
		}
	}
	return "", i18n.Errorf("运营后台项目里没有 %s 表", "The back-office project has no %s table", key)
}

func tablePath(projectID, tableID, suffix string) string {
	return fmt.Sprintf("/api/p/project/%s/table/%s/%s", url.PathEscape(projectID), url.PathEscape(tableID), suffix)
}

// RecordQuery 是 record_list 的查询：where 是字段相等，Filter 是其余条件（neq / in / gt / gte / lt / lte / between，AND），
// 过滤、排序都在服务端做。单页最多 1000 条；OrderBy 是 "id asc" 时可以用 Cursor 一页页往后读。
type RecordQuery struct {
	Where     map[string]any `json:"where,omitempty"`
	Filter    *RecordFilter  `json:"filter,omitempty"`
	OrderBy   string         `json:"order_by,omitempty"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset,omitempty"`
	Cursor    string         `json:"cursor,omitempty"`
	SkipTotal bool           `json:"skip_total"`
}

type RecordFilter struct {
	Conditions []RecordCondition `json:"conditions"`
}

// RecordCondition 的 FieldID 是 body 里的字段名。
type RecordCondition struct {
	FieldID  string `json:"fieldId"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// QueryRecords 查一页。total 是满足条件的总行数；q.SkipTotal 时是 -1（不数，大表上省一次 COUNT）。
func (c *Client) QueryRecords(ctx context.Context, projectID, tableID string, q RecordQuery) (list []Record, next string, total int64, err error) {
	var ret struct {
		List       []Record `json:"list"`
		NextCursor string   `json:"next_cursor"`
		Total      int64    `json:"total"`
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 1000
	}
	err = c.do(ctx, "POST", tablePath(projectID, tableID, "record_list"), nil, q, &ret)
	if q.SkipTotal {
		ret.Total = -1
	}
	return ret.List, ret.NextCursor, ret.Total, err
}

// ListRecords 读完整张表（满足 q 的条件的全部行）：按 id 游标一页页读，不受单页 1000 条的限制。
func (c *Client) ListRecords(ctx context.Context, projectID, tableID string, q RecordQuery) ([]Record, error) {
	q.OrderBy, q.Limit, q.SkipTotal, q.Cursor = "id asc", 1000, true, ""
	var all []Record
	for page := 0; page < 1000; page++ { // 最多 100 万行，防止接口异常时死循环
		list, next, _, err := c.QueryRecords(ctx, projectID, tableID, q)
		if err != nil {
			return nil, err
		}
		all = append(all, list...)
		if next == "" || len(list) < q.Limit {
			break
		}
		q.Cursor = next
	}
	return all, nil
}

// AggregateRecords 是 record_aggregate：按字段（时间可截到 day / week / month / year）分组，算 count / sum / avg / min / max / first / last。
// req 原样发给平台（where、filter、group_by、metrics、order_by、limit、timezone），返回每组一行和是否可能被截断。
func (c *Client) AggregateRecords(ctx context.Context, projectID, tableID string, req map[string]any) (list []map[string]any, truncated bool, err error) {
	var ret struct {
		List      []map[string]any `json:"list"`
		Truncated bool             `json:"truncated"`
	}
	err = c.do(ctx, "POST", tablePath(projectID, tableID, "record_aggregate"), nil, req, &ret)
	return ret.List, ret.Truncated, err
}

func (c *Client) CreateRecord(ctx context.Context, projectID, tableID string, body any) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var ret struct {
		ID string `json:"id"`
	}
	err = c.do(ctx, "POST", tablePath(projectID, tableID, "record"), nil, map[string]any{"body": json.RawMessage(b)}, &ret)
	return ret.ID, err
}

func (c *Client) DeleteRecord(ctx context.Context, projectID, tableID, recordID string) error {
	return c.do(ctx, "DELETE", tablePath(projectID, tableID, "record"), nil, map[string]string{"id": recordID}, nil)
}

func (c *Client) GetRecord(ctx context.Context, projectID, tableID, recordID string) (*Record, error) {
	var r Record
	err := c.Get(ctx, tablePath(projectID, tableID, "record"), url.Values{"id": {recordID}}, &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRecord 整体替换一条记录的 body。
func (c *Client) UpdateRecord(ctx context.Context, projectID, tableID, recordID string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.do(ctx, "PUT", tablePath(projectID, tableID, "record"), nil, map[string]any{"id": recordID, "body": json.RawMessage(b)}, nil)
}

// TableKeys 返回项目里已有的表 key。
func (c *Client) TableKeys(ctx context.Context, projectID string) (map[string]bool, error) {
	var ret struct {
		List []struct {
			Key string `json:"key"`
		} `json:"list"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/api/p/project/%s/table_list", url.PathEscape(projectID)), url.Values{"limit": {"200"}}, &ret); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range ret.List {
		out[t.Key] = true
	}
	return out, nil
}

func (c *Client) CreateTable(ctx context.Context, projectID, key, name, desc string, schema json.RawMessage) error {
	return c.do(ctx, "POST", fmt.Sprintf("/api/p/project/%s/table", url.PathEscape(projectID)), nil,
		map[string]any{"key": key, "name": name, "desc": desc, "json_schema": schema}, nil)
}
