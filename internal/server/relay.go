package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/relay"
	"github.com/annulo/annulo/internal/version"
)

// 中转（internal/relay）：手机上打开运营后台点「发布」，站点 Func 经平台转到这台电脑，在这里执行本机函数。
// 这台电脑上的项目都能调（~/.shuttle/backends/<id>，和当前打开的项目在同一个 creght 集群的），不用先在电脑上切过去；
// 只执行声明了 export const remote 的函数，执行方式和页面按钮（local/run）一样。

func (s *Server) newRelay() *relay.Client {
	m := machine(s.cfg.Dir)
	return relay.New(relay.Host{
		APIHost: func() string {
			if !s.ready.Load() {
				return ""
			}
			return s.ws.APIHost
		},
		Token: func() string {
			if !s.ready.Load() {
				return ""
			}
			tok, _ := creght.ReadToken(s.ws.APIHost)
			return tok
		},
		MachineID:   fmt.Sprint(m["id"]),
		MachineName: fmt.Sprint(m["name"]),
		Projects:    s.relayProjects,
		Remote:      s.relayRemote,
		Enabled:     func() bool { return s.cfg.RemoteCalls },
		Run:         s.relayRun,
	})
}

// relayProjects 是这台电脑上能被调到的项目：本机有副本、和当前项目在同一个 creght 集群（中转连的是这个集群）。
func (s *Server) relayProjects() []string {
	if !s.ready.Load() {
		return nil
	}
	out := []string{}
	if !s.ws.Offline { // 离线项目不在 creght 上，手机调不到
		out = append(out, s.ws.ProjectID)
	}
	ents, _ := os.ReadDir(filepath.Join(s.cfg.Dir, "backends"))
	dirs := []string{}
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(s.cfg.Dir, "backends", e.Name()))
		}
	}
	if s.cfg.LegacyProject() != "" {
		dirs = append(dirs, filepath.Join(s.cfg.Dir, "backend"))
	}
	for _, d := range dirs {
		ws, err := creght.OpenWorkspace(d)
		if err != nil || ws.Offline || ws.APIHost != s.ws.APIHost || slices.Contains(out, ws.ProjectID) {
			continue
		}
		out = append(out, ws.ProjectID)
	}
	return out
}

// relayRemote 是每个项目里声明了 remote 的函数，报给平台（手机页面据此决定按钮能不能点）。
func (s *Server) relayRemote() map[string][]string {
	out := map[string][]string{}
	for _, pid := range s.relayProjects() {
		dir := s.ws.Dir
		if pid != s.ws.ProjectID {
			dir = s.cfg.DirFor(pid)
		}
		fns, _ := localfn.RemoteFns(dir)
		if fns == nil {
			fns = []string{}
		}
		if pid == s.ws.ProjectID {
			fns = append(fns, remoteAIFns...) // 助手（remoteai.go）：只给电脑上正在打开的项目
		}
		out[pid] = fns
	}
	return out
}

func (s *Server) relayRun(ctx context.Context, projectID, fn string, input any, emit func(any)) (any, error) {
	if !s.ready.Load() {
		return nil, i18n.New("这台电脑上的 Annulo 还没打开项目", "Annulo on this computer hasn't opened a project yet")
	}
	if fn = brand.RemoteName(fn); strings.HasPrefix(fn, remoteAIPrefix) { // _annulo.xxx 当作 _shuttle.xxx
		return s.relayAI(ctx, projectID, fn, input)
	}
	if creght.IsOfflineID(projectID) { // 离线项目不在 creght 上，平台转不来；转来了也不跑
		return nil, i18n.New("离线项目不支持远程访问", "Offline projects don't support remote access")
	}
	host, err := s.projectHost(projectID)
	if err != nil {
		return nil, err
	}
	ok, err := localfn.IsRemote(host.WorkDir, fn)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, i18n.Errorf("%s 没有声明 remote，不能从手机上调：在它的文件里加进 export const remote", "%s isn't declared in remote, so it can't be called from the phone: add it to export const remote in its file", fn)
	}
	host.RunID = fmt.Sprintf("m%d", time.Now().UnixNano()) // 日志里能分出是手机上发起的
	return localfn.Run(ctx, host, fn, input, func(e localfn.Event) {
		if e.Type == "progress" {
			emit(e.Data)
		}
	})
}

// projectHost 是在某个项目里跑本机函数的环境：当前项目就是 localHost；别的项目换成它的目录、表、日志，
// 浏览器登录态、密钥、连接、MCP 是整台电脑共用的。
func (s *Server) projectHost(projectID string) (localfn.Host, error) {
	if projectID == s.ws.ProjectID {
		return s.localHost(), nil
	}
	ws, err := creght.OpenWorkspaceOn(s.cfg.DirFor(projectID), s.ws.APIHost)
	if err != nil || ws.ProjectID != projectID {
		return localfn.Host{}, i18n.Errorf("这台电脑上没有项目 %s：在电脑上的 Annulo 里打开它一次", "This computer doesn't have project %s: open it once in Annulo on the computer", projectID)
	}
	if !ws.Offline && ws.APIHost != s.ws.APIHost {
		return localfn.Host{}, i18n.New("这个项目在别的 creght 区域：在电脑上的 Annulo 里切到那个区域再试", "This project is in another creght region: switch to it in Annulo on the computer and try again")
	}
	h := s.localHost()
	logs := filepath.Join(s.cfg.Dir, "logs", "local", ws.ProjectID)
	h.WorkDir, h.LogDir = ws.Dir, logs
	h.SnapshotDir = filepath.Join(s.cfg.Dir, "snapshots", ws.ProjectID)
	h.Workspace = workspaceInfo(map[string]any{"project_id": ws.ProjectID, "site_id": ws.SiteID, "api_host": ws.APIHost, "shuttle_projects": s.shuttleProjects(), "shuttle_api": version.API, "logs_dir": logs, "machine": machine(s.cfg.Dir), "offline": ws.Offline})
	h.DB = localDB{s.otherProject(ws)}
	return h, nil
}

// otherProjects 缓存别的项目的表 id 和表声明（按项目和它的目录：同一个 id 换了目录，表声明、数据库都不是原来那份），
// 手机上连着发几次不用每次都查。
var otherProjects sync.Map // projectID + "\x00" + dir → *projectDB

func (s *Server) otherProject(ws *creght.Workspace) *projectDB {
	key := ws.ProjectID + "\x00" + ws.Dir
	if v, ok := otherProjects.Load(key); ok {
		return v.(*projectDB)
	}
	tables := &workspaceTables{}
	isTable := func(key string) bool {
		defs, _, _ := tables.load(ws.Dir)
		for _, d := range defs {
			if d.Key == key {
				return true
			}
		}
		return false
	}
	tableErr := func(fallback error) error {
		if _, _, err := tables.load(ws.Dir); err != nil {
			return err
		}
		return fallback
	}
	var store tableStore = creght.NewClient(ws.APIHost)
	if ws.Offline { // 离线项目的表在它自己的 SQLite 里
		db, err := localdb.Open(s.offlineDBPath(ws.ProjectID))
		if err != nil {
			log.Printf("打开离线项目 %s 的数据库失败：%v", ws.ProjectID, err)
		} else {
			store = db
		}
	}
	p := &projectDB{c: store, projectID: ws.ProjectID, tids: &tableIDs{}, isTable: isTable, tableErr: tableErr}
	v, _ := otherProjects.LoadOrStore(key, p)
	return v.(*projectDB)
}

// 设置 → 远程访问：GET 开关和连接状态，PUT { enabled } 打开 / 关闭（默认关）。
func (s *Server) apiRemoteSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var in struct{ Enabled bool }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		s.cfg.RemoteCalls = in.Enabled
		if err := s.cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.relay.Reconnect()
		if in.Enabled { // 给它一点时间连上，界面上马上就能看到「已连上」
			for i := 0; i < 30; i++ {
				if online, _ := s.relay.Status(); online {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	online, lastErr := s.relay.Status()
	m := machine(s.cfg.Dir)
	writeJSON(w, map[string]any{"enabled": s.cfg.RemoteCalls, "online": online, "error": lastErr, "machine": m["name"], "offline_project": s.ready.Load() && s.ws.Offline, "projects": s.remoteAddresses()})
}

// remoteAddresses 是能远程调到的每个项目在手机、别的电脑上打开的地址（项目站点的预览域名，creght 账号登录）。
// 开关是这台电脑全局的，地址是每个项目各一个，所以全列出来；当前项目排第一。
func (s *Server) remoteAddresses() []map[string]any {
	out := []map[string]any{}
	for _, pid := range s.relayProjects() {
		siteID, dir := s.ws.SiteID, s.ws.Dir
		if pid != s.ws.ProjectID {
			ws, err := creght.OpenWorkspace(s.cfg.DirFor(pid))
			if err != nil {
				continue
			}
			siteID, dir = ws.SiteID, ws.Dir
		}
		out = append(out, map[string]any{"project_id": pid, "name": s.projectName(pid, dir), "url": creght.PreviewURL(s.ws.APIHost, siteID), "current": pid == s.ws.ProjectID})
	}
	return out
}
