package server

import (
	"context"
	"sort"
	"sync"
	"time"
)

// 业务表变了就告诉打开着的页面：页面按表重读数据，不用等助手说完一轮、也不用猜它跑的命令改没改数据。
//
// 写表只有一处入口（projectDB 的 create / patch / delete）：页面的 db/<表>、本机函数的 ctx.db（按钮、定时任务、助手跑的 annulo run）都走它。
// 写成功后记下表名，合并 dataChangeDelay 再通过页面已经连着的推送（localsite 的 /_client/events）发：
//
//	{ type: "db", tables: ["assets", "articles"], others: true }
//
// 页面里的运行时把每张表转成 window 的 db:changed 事件（detail.table），模板按自己用到的表重读。
// others：这批里有不是页面自己写的（本机函数、助手、定时任务）。老模板只认外壳发的 shuttle:refresh，运行时在 others 时也发一个给它们。
// creght 在线项目里绕过 Annulo 写的（编辑器、别的设备、creght 命令行）这里看不到。

const dataChangeDelay = 300 * time.Millisecond

type dataChanges struct {
	mu     sync.Mutex
	tables map[string]bool
	others bool
	timer  *time.Timer
}

type pageWriteKey struct{}

// byPage 标记这次写是页面自己发起的（db/<表> 接口）。
func byPage(ctx context.Context) context.Context { return context.WithValue(ctx, pageWriteKey{}, true) }

func (s *Server) dataChanged(ctx context.Context, table string) {
	c := &s.changes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tables == nil {
		c.tables = map[string]bool{}
	}
	c.tables[table] = true
	if ctx.Value(pageWriteKey{}) == nil {
		c.others = true
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(dataChangeDelay, s.flushDataChanges)
	}
}

func (s *Server) flushDataChanges() {
	c := &s.changes
	c.mu.Lock()
	tables := make([]string, 0, len(c.tables))
	for t := range c.tables {
		tables = append(tables, t)
	}
	others := c.others
	c.tables, c.others, c.timer = nil, false, nil
	c.mu.Unlock()
	sort.Strings(tables)
	if s.site != nil && len(tables) > 0 {
		s.site.Notify(map[string]any{"type": "db", "tables": tables, "others": others})
	}
}
