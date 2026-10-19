package server

import (
	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

// newTestServer 拼一个测试用的视图：cfg 在 core，ws / agent 在 project（没给 agent 时 project 也没有）。
func newTestServer(cfg *config.Config, ws *creght.Workspace, ag *agent.Agent) *Server {
	c := &core{cfg: cfg, root: ag, projects: map[string]*project{}}
	c.none = &project{agent: ag, runs: map[string]*agentRun{}}
	p := c.none
	if ws != nil {
		p = &project{ws: ws, agent: ag, runs: map[string]*agentRun{}}
		c.projects[ws.ProjectID] = p // 打开着的项目（测试里自己 ready.Store(true)）
	}
	return &Server{core: c, project: p}
}
