package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/annulo/annulo/internal/i18n"

	piagent "github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"

	"github.com/annulo/annulo/internal/localfn"
)

// agent 读业务表的工具：db_query / db_aggregate，参数和本机函数的 ctx.db.query / aggregate 一样。
//
// 为什么不让 agent 用 creght CLI 读：`creght table record list` 默认只回 20 条，--filter 要先写成 JSON 文件、
// 键名写错会被平台静默忽略，agent 常常全拉下来再用 jq 自己筛（表一大就漏数据）。
// 工具的参数有 schema，运营后台站点和能碰的表由 Shuttle 定，写错直接报错。
// 只读：写业务表仍然走本机函数（按钮、定时任务）或 creght CLI。

// dbToolMaxLimit 是一次最多回给模型的行数：行是要进上下文的，要更多就翻页，要合计用 db_aggregate
const dbToolMaxLimit = 200

const condSchema = `{"type":"array","description":"条件（AND）。每项 { field, op, value }：field 是业务字段名（带不带 body. 都行），op 是 eq / neq / in / gt / gte / lt / lte / between（value 为 [起, 止]，两端都含）。数字按数值比，字符串按字典序比（YYYY-MM-DD、ISO 时间能直接比）",
  "items":{"type":"object","properties":{"field":{"type":"string"},"op":{"type":"string","enum":["eq","neq","in","gt","gte","lt","lte","between"]},"value":{}},"required":["field","op","value"],"additionalProperties":false}}`

const dbQuerySchema = `{"type":"object","properties":{
  "table":{"type":"string","description":"业务表的 key（tables/<key>.json 的文件名）"},
  "where":{"type":"object","description":"字段相等，如 {\"channel_id\": \"p9...\"}"},
  "filter":` + condSchema + `,
  "order_by":{"type":"string","description":"如 \"views desc\"、\"published_at desc\"；默认新建的在前。和 cursor 不能同时用"},
  "limit":{"type":"integer","description":"默认 20，最多 200"},
  "fields":{"type":"array","items":{"type":"string"},"description":"只返回这些字段（id 总会带上）。长正文的表（文章、推文）只要几个字段时传它，省上下文"},
  "cursor":{"type":"string","description":"按 id 顺序翻页读全表：第一页传 \"\"，之后传上一页的 next_cursor，它为空就读完了"}
},"required":["table"],"additionalProperties":false}`

const dbAggregateSchema = `{"type":"object","properties":{
  "table":{"type":"string","description":"业务表的 key（tables/<key>.json 的文件名）"},
  "where":{"type":"object","description":"字段相等"},
  "filter":` + condSchema + `,
  "group_by":{"type":"array","description":"分组：字段名，或 { field, trunc: day | week | month | year, as }（按时间截断）。不传就是整张表一组","items":{}},
  "metrics":{"type":"array","description":"[{ op: count | sum | avg | min | max | first | last, field, as, order_by }]；first / last 是组内按 order_by 排序后的首尾值","items":{"type":"object"}},
  "order_by":{"type":"string","description":"按分组字段或 metrics 的 as 排序，如 \"views desc\""},
  "limit":{"type":"integer","description":"最多返回多少组"},
  "timezone":{"type":"string","description":"按天 / 月截断用的时区，默认 Asia/Shanghai"}
},"required":["table","metrics"],"additionalProperties":false}`

func (s *Server) dbTools() []piagent.AgentTool {
	return []piagent.AgentTool{
		{
			Name:  "db_query",
			Label: "查业务表",
			Description: "查项目的业务表（tables/ 里声明的）。已知记录 id 用 where: { id: \"…\" }，读取系统主键；filter 的 id eq 也支持。其他过滤、排序在平台上做。" +
				"找「最高 / 最近 / 某段时间内」的记录用 filter + order_by + limit，不要全拉下来自己筛。只要合计、分组统计用 db_aggregate。" +
				"返回 { list, has_more, next_cursor }；has_more 为 true 说明还有没返回的行。只读，写表走本机函数。",
			Parameters: mustSchema(dbQuerySchema),
			Execute: func(ctx context.Context, _ string, p map[string]any, _ piagent.ToolUpdateFunc) (piagent.AgentToolResult, error) {
				out, err := s.dbToolQuery(ctx, p)
				if err != nil {
					return piagent.AgentToolResult{}, err
				}
				return jsonResult(out), nil
			},
		},
		{
			Name:  "db_aggregate",
			Label: "统计业务表",
			Description: "在平台上对业务表分组汇总（count / sum / avg / min / max / first / last），行再多也不用拉回来。" +
				"按天 / 按月合计、算某段时间涨了多少、每个类别多少条，用它。返回 { list, truncated }。",
			Parameters: mustSchema(dbAggregateSchema),
			Execute: func(ctx context.Context, _ string, p map[string]any, _ piagent.ToolUpdateFunc) (piagent.AgentToolResult, error) {
				out, err := s.dbToolAggregate(ctx, p)
				if err != nil {
					return piagent.AgentToolResult{}, err
				}
				return jsonResult(out), nil
			},
		},
	}
}

func (s *Server) dbToolTable(p map[string]any) (string, error) {
	if s.ws == nil {
		return "", i18n.New("还没有打开项目", "No project is open")
	}
	table, _ := p["table"].(string)
	if !s.isOpsTable(table) {
		defs, _ := s.tableDefs()
		keys := make([]string, len(defs))
		for i, d := range defs {
			keys[i] = d.Key
		}
		return "", s.tableErr(i18n.Errorf("没有业务表 %q；能查的表：%s", "No data table %q; available tables: %s", table, strings.Join(keys, ", ")))
	}
	return table, nil
}

func (s *Server) dbToolQuery(ctx context.Context, p map[string]any) (map[string]any, error) {
	table, err := s.dbToolTable(p)
	if err != nil {
		return nil, err
	}
	filter, err := localfn.ParseConds(p["filter"])
	if err != nil {
		return nil, err
	}
	where, _ := p["where"].(map[string]any)
	order, _ := p["order_by"].(string)
	limit := 20
	if n, ok := p["limit"].(float64); ok && n > 0 {
		limit = min(int(n), dbToolMaxLimit)
	}
	// 多取一条，才知道后面还有没有（正好 limit 条时不误报 has_more）
	q := localfn.Query{Where: where, Filter: filter, OrderBy: order, Limit: limit + 1}
	if c, ok := p["cursor"].(string); ok {
		if order != "" && order != "id asc" {
			return nil, i18n.Errorf("用 cursor 翻页时按 id 顺序读，不能同时指定 order_by（%q）；要排序就别翻页，要合计用 db_aggregate", "Paging with cursor reads in id order, so order_by (%q) can't be set at the same time; don't page if you need sorting, and use db_aggregate for totals", order)
		}
		q.Cursor, q.Limit = &c, limit
	}
	list, next, _, err := localDB{s.cur()}.Query(ctx, table, q)
	if err != nil {
		return nil, err
	}
	more := len(list) > limit
	list = list[:min(len(list), limit)]
	if fs, ok := p["fields"].([]any); ok && len(fs) > 0 {
		for i, r := range list {
			m := map[string]any{"id": r["id"]}
			for _, f := range fs {
				k := strings.TrimPrefix(fmt.Sprint(f), "body.")
				if v, ok := r[k]; ok {
					m[k] = v
				}
			}
			list[i] = m
		}
	}
	if list == nil {
		list = []map[string]any{}
	}
	out := map[string]any{"list": list, "has_more": more}
	if q.Cursor != nil {
		out["next_cursor"] = next
		out["has_more"] = next != ""
	}
	return out, nil
}

func (s *Server) dbToolAggregate(ctx context.Context, p map[string]any) (map[string]any, error) {
	table, err := s.dbToolTable(p)
	if err != nil {
		return nil, err
	}
	filter, err := localfn.ParseConds(p["filter"])
	if err != nil {
		return nil, err
	}
	req := map[string]any{}
	for k, v := range p {
		if k != "table" && k != "filter" {
			req[k] = v
		}
	}
	list, truncated, err := localDB{s.cur()}.Aggregate(ctx, table, req, filter)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []map[string]any{}
	}
	return map[string]any{"list": list, "truncated": truncated}, nil
}

func mustSchema(s string) *ai.Schema {
	var sc ai.Schema
	if err := json.Unmarshal([]byte(s), &sc); err != nil {
		panic("工具参数的 schema 写错了：" + err.Error())
	}
	return &sc
}

func jsonResult(v any) piagent.AgentToolResult {
	b, _ := json.Marshal(v)
	return piagent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(b)}}}
}
