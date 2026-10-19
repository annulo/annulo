package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/tunnel"
)

// 远程访问的隧道（docs/mobile-remote.md「远程访问：隧道」）：手机打开 https://<随机串>.remote.creght.cn，
// creght 登录、认出是这台电脑，把请求经隧道（internal/tunnel）转到这里，交给和本机一样的 handler。
//
// 隧道进来的请求在 context 里标成远程请求（remoteOf），门卫按它换规则：
//   - 不查 Host 是不是 localhost（Host 是远程域名），Origin 照样要和 Host 一致、API 照样要自定义头；
//   - 平台在请求头里带上账号和设备（X-Annulo-Remote-User / -Device，平台先删掉手机自己带的同名头），
//     没在电脑上确认过的设备只能看到「等电脑上确认」；
//   - 转到本机任意端口的 /_shuttle/port/、浏览器插件的入口、给外部 agent 的 MCP 不对远程开；
//     在电脑上开窗口的（local/open、拉起 Chrome）也不开，本机函数的 ctx.fs.canOpen 是 false；
//     确认设备、开关隧道只能在电脑上做。
//
// 能远程进来的人能用助手，等于能在这台电脑上执行命令，所以除了平台的登录，每台新设备还要电脑上点一次确认。

// 平台转来的请求头（weave docs/architecture/remote-tunnel.md「反向代理带给 Annulo 的头」）
const (
	remoteUserHeader       = "X-Annulo-Remote-User"
	remoteDeviceHeader     = "X-Annulo-Remote-Device"
	remoteDeviceNameHeader = "X-Annulo-Remote-Device-Name"
	remoteFlagHeader       = "X-Annulo-Remote"
)

type remoteKey struct{}

// remoteReq 是一个远程请求是谁发的（平台告诉的）。
type remoteReq struct {
	User   string
	Device string
	Name   string // 设备的样子（User-Agent 里看出来的），给电脑上确认时看
}

// remoteOf：隧道进来的请求返回它的来源；本机请求是 nil。
func remoteOf(r *http.Request) *remoteReq {
	v, _ := r.Context().Value(remoteKey{}).(*remoteReq)
	return v
}

func (s *Server) newTunnel() *tunnel.Client {
	m := machine(s.cfg.Dir)
	return tunnel.New(tunnel.Host{
		APIHost: func() string {
			if !creght.Has(s.creghtHost(), "remote_tunnel") {
				return "" // 这个 creght 没有远程访问（自己部署的、老版本）：不连
			}
			return s.creghtHost()
		},
		Token: func() string {
			tok, _ := creght.ReadToken(s.creghtHost())
			return tok
		},
		MachineID:   fmt.Sprint(m["id"]),
		MachineName: fmt.Sprint(m["name"]),
		Projects:    func() []string { return localProjects(s.cfg) },
		Enabled:     func() bool { return s.cfg.RemoteTunnel },
		OnConnect:   func() { s.remoteURLs.refresh(s) },
	})
}

// serveTunnel 接住平台经隧道转来的请求：标成远程请求，再交给和本机一样的 handler。
func (s *Server) serveTunnel() {
	srv := &http.Server{Handler: s.remoteHandler(s.Handler()), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(s.tunnel.Listener()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("隧道：停止接收请求：%v", err)
		}
	}()
}

func (s *Server) remoteHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rq := &remoteReq{User: r.Header.Get(remoteUserHeader), Device: r.Header.Get(remoteDeviceHeader), Name: r.Header.Get(remoteDeviceNameHeader)}
		if rq.Name == "" {
			rq.Name = deviceName(r.UserAgent())
		}
		for _, h := range []string{remoteUserHeader, remoteDeviceHeader, remoteDeviceNameHeader, remoteFlagHeader} {
			r.Header.Del(h)
		}
		if rq.Device == "" || len(rq.Device) > 128 {
			http.Error(w, "missing device", http.StatusForbidden) // 平台没带设备：不是平台转来的正常请求
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), remoteKey{}, rq)))
	})
}

// remoteGuard 是门卫对远程请求的那部分：设备没确认过、碰了不对远程开的路径，写好响应返回 false。
func (s *Server) remoteGuard(w http.ResponseWriter, r *http.Request, rq *remoteReq) bool {
	if !s.cfg.RemoteTunnel { // 刚在电脑上关掉，隧道还没断
		http.Error(w, i18n.T("这台电脑关掉了远程访问", "Remote access is turned off on this computer"), http.StatusForbidden)
		return false
	}
	d := s.devices().seen(rq)
	if d.Status != deviceApproved {
		if strings.HasPrefix(r.URL.Path, "/_shuttle/api/") {
			w.Header().Set("content-type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": i18n.T("等电脑上确认这台设备", "Waiting for this device to be approved on the computer"), "code": "device_pending"})
			return false
		}
		pendingPage(w, d)
		return false
	}
	p := r.URL.Path
	api := strings.TrimPrefix(p, "/_shuttle/api/")
	switch {
	case strings.HasPrefix(p, portURL), p == "/_shuttle/mcp", strings.HasPrefix(p, "/_shuttle/api/browser/ext"),
		strings.HasPrefix(api, "remote/") && r.Method != http.MethodGet,
		api == "local/open", // 用系统程序打开、在访达里显示：会在电脑上开窗口，手机上的人看不到
		api == "settings/browser/chrome",
		strings.HasPrefix(api, "settings/remote") && r.Method != http.MethodGet:
		fail(w, http.StatusForbidden, i18n.New("远程访问时不能用：在电脑上操作", "Not available over remote access: do it on the computer"))
		return false
	}
	return true
}

// pendingPage：新设备第一次打开，等电脑上点确认。每 3 秒刷新一次，确认了就进去。
func pendingPage(w http.ResponseWriter, d device) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	title := i18n.T("等电脑上确认", "Waiting for approval")
	body := i18n.T("这台设备第一次远程打开 Annulo。到电脑上的 Annulo 里点「允许」，这里会自动进去。", "This device is opening Annulo remotely for the first time. Click \"Allow\" in Annulo on the computer and this page will continue automatically.")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="3"><title>%s</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font:15px/1.6 -apple-system,system-ui,sans-serif;background:#fafafa;color:#222;padding:16px;box-sizing:border-box}
main{max-width:360px;text-align:center}h1{font-size:18px;margin:0 0 8px}p{margin:0 0 12px;color:#555}code{font-size:13px;background:#eee;border-radius:6px;padding:2px 6px}
@media (prefers-color-scheme:dark){body{background:#111;color:#eee}p{color:#aaa}code{background:#222}}</style></head>
<body><main><h1>%s</h1><p>%s</p><p><code>%s</code></p></main></body></html>`, title, title, body, html.EscapeString(d.Name))
}

// deviceName 从 User-Agent 看出是什么设备、什么浏览器，给电脑上确认时认。
func deviceName(ua string) string {
	osName := ""
	for _, k := range []struct{ key, name string }{{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Macintosh", "Mac"}, {"Windows", "Windows"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, k.key) {
			osName = k.name
			break
		}
	}
	br := ""
	for _, k := range []struct{ key, name string }{{"MicroMessenger", "微信"}, {"EdgA/", "Edge"}, {"Edg/", "Edge"}, {"CriOS", "Chrome"}, {"FxiOS", "Firefox"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}} {
		if strings.Contains(ua, k.key) {
			br = k.name
			break
		}
	}
	switch {
	case osName != "" && br != "":
		return osName + " · " + br
	case osName != "":
		return osName
	case br != "":
		return br
	}
	return i18n.T("未知设备", "Unknown device")
}

// remoteProjectURL 是项目的远程地址（https://<随机串>.remote.creght.cn/，外壳就在根路径上）；还没查到时是空。
func (s *Server) remoteProjectURL(pid string) string {
	if u := s.remoteURLs.get()[pid]; u != "" {
		return strings.TrimRight(u, "/") + "/"
	}
	return ""
}

// remoteURLCache 是这台电脑各项目的远程地址：平台在隧道握手时建好，从 GET /api/u/remote/machines 查（连上时、打开设置页时）。
type remoteURLCache struct {
	mu   sync.Mutex
	urls map[string]string // 项目 id → https://<随机串>.remote.creght.cn
	at   time.Time         // 上次查的时间：设置页一直在轮询，30 秒内不重复查

	sub   map[string]any // 订阅，同样缓存 30 秒
	subAt time.Time
}

func (c *remoteURLCache) get() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.urls
}

// refreshSoon：30 秒内查过就不再查（设置页轮询用）；refresh 总是查（刚连上时）。
func (c *remoteURLCache) refreshSoon(s *Server) {
	c.mu.Lock()
	fresh := time.Since(c.at) < 30*time.Second
	c.mu.Unlock()
	if !fresh {
		c.refresh(s)
	}
}

func (c *remoteURLCache) refresh(s *Server) {
	c.mu.Lock()
	c.at = time.Now()
	c.mu.Unlock()
	var out struct {
		List []struct {
			MachineID string            `json:"machine_id"`
			Projects  map[string]string `json:"projects"`
		} `json:"list"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := creght.NewClient(s.creghtHost()).Get(ctx, "/api/u/remote/machines", nil, &out); err != nil {
		log.Printf("隧道：查项目的远程地址失败：%v", err)
		return
	}
	id := fmt.Sprint(machine(s.cfg.Dir)["id"])
	for _, m := range out.List {
		if m.MachineID == id {
			c.mu.Lock()
			c.urls = m.Projects
			c.mu.Unlock()
			return
		}
	}
}

// remoteSubscription 是平台上这个账号的远程访问订阅（GET /api/u/remote/subscription）：状态、额度、本月用量、开通页。
// 30 秒内查过就用上次的；fresh 为 true 时总是查（刚开关过）。
func (s *Server) remoteSubscription(ctx context.Context, fresh bool) (map[string]any, error) {
	c := &s.remoteURLs
	c.mu.Lock()
	if !fresh && c.sub != nil && time.Since(c.subAt) < 30*time.Second {
		defer c.mu.Unlock()
		return c.sub, nil
	}
	c.mu.Unlock()
	var out map[string]any
	if err := creght.NewClient(s.creghtHost()).Get(ctx, "/api/u/remote/subscription", nil, &out); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sub, c.subAt = out, time.Now()
	c.mu.Unlock()
	return out, nil
}

// ---- 设备 ----

const (
	devicePending  = "pending"
	deviceApproved = "approved"
	maxPending     = 10 // 等确认的最多留几台：多了把最早的挤掉
	activeWindow   = 90 * time.Second
)

type device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	User     string `json:"user,omitempty"`
	Status   string `json:"status"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen"`
}

// deviceStore 存在 <数据目录>/remote-devices.json：确认过的设备和等确认的。
type deviceStore struct {
	file  string
	mu    sync.Mutex
	list  []device
	saved int64 // 上次写盘：最后在线时间一分钟最多写一次
}

var deviceStores sync.Map // 数据目录 → *deviceStore

func (s *Server) devices() *deviceStore {
	if v, ok := deviceStores.Load(s.cfg.Dir); ok {
		return v.(*deviceStore)
	}
	st := &deviceStore{file: filepath.Join(s.cfg.Dir, "remote-devices.json")}
	if b, err := os.ReadFile(st.file); err == nil {
		_ = json.Unmarshal(b, &st.list)
	}
	v, _ := deviceStores.LoadOrStore(s.cfg.Dir, st)
	return v.(*deviceStore)
}

// seen 记下这台设备来过：没见过的加进等确认；返回它现在的状态。
func (d *deviceStore) seen(rq *remoteReq) device {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	for i := range d.list {
		if d.list[i].ID == rq.Device {
			d.list[i].LastSeen = now
			if rq.User != "" {
				d.list[i].User = rq.User
			}
			if now-d.saved > 60 {
				d.saveLocked()
			}
			return d.list[i]
		}
	}
	pending := 0
	for _, x := range d.list {
		if x.Status == devicePending {
			pending++
		}
	}
	if pending >= maxPending {
		for i, x := range d.list {
			if x.Status == devicePending {
				d.list = slices.Delete(d.list, i, i+1)
				break
			}
		}
	}
	nd := device{ID: rq.Device, Name: rq.Name, User: rq.User, Status: devicePending, Created: now, LastSeen: now}
	d.list = append(d.list, nd)
	d.saveLocked()
	return nd
}

func (d *deviceStore) saveLocked() {
	d.saved = time.Now().Unix()
	b, _ := json.MarshalIndent(d.list, "", "  ")
	if err := os.WriteFile(d.file, b, 0o600); err != nil {
		log.Printf("保存远程设备失败：%v", err)
	}
}

func (d *deviceStore) all() []device {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.list)
}

// approve 把等确认的设备改成确认过；remove 拒绝或撤销（下次再来又是等确认）。
func (d *deviceStore) approve(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.list {
		if d.list[i].ID == id {
			d.list[i].Status = deviceApproved
			d.saveLocked()
			return true
		}
	}
	return false
}

func (d *deviceStore) remove(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.list {
		if d.list[i].ID == id {
			d.list = slices.Delete(d.list, i, i+1)
			d.saveLocked()
			return true
		}
	}
	return false
}

// ---- 接口 ----

// apiRemoteDevices：
//
//	GET    remote/devices               确认过的和等确认的设备，active 是最近在用的（顶栏提示「正在远程访问」）
//	POST   remote/devices/<id>/approve  允许（只能在电脑上）
//	DELETE remote/devices/<id>          拒绝或撤销
func (s *Server) apiRemoteDevices(w http.ResponseWriter, r *http.Request, rest string) {
	st := s.devices()
	if rest == "" && r.Method == http.MethodGet {
		now := time.Now().Unix()
		list := []map[string]any{}
		for _, d := range st.all() {
			list = append(list, map[string]any{"id": d.ID, "name": d.Name, "user": d.User, "status": d.Status, "created": d.Created, "last_seen": d.LastSeen,
				"active": d.Status == deviceApproved && now-d.LastSeen < int64(activeWindow/time.Second)})
		}
		writeJSON(w, map[string]any{"enabled": s.cfg.RemoteTunnel, "list": list})
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case action == "approve" && r.Method == http.MethodPost:
		if !st.approve(id) {
			fail(w, http.StatusNotFound, i18n.New("没有这台设备", "No such device"))
			return
		}
	case action == "" && r.Method == http.MethodDelete:
		st.remove(id)
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个接口：%s remote/devices/%s", "No such endpoint: %s remote/devices/%s", r.Method, rest))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiRemoteTunnel：设置 → 远程访问 的隧道部分。GET 开关、连接状态、平台拒绝的原因；PUT { enabled } 打开 / 关闭（默认关）。
func (s *Server) apiRemoteTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var in struct{ Enabled bool }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		s.cfg.RemoteTunnel = in.Enabled
		if err := s.cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.tunnel.Reconnect()
		if in.Enabled {
			for i := 0; i < 30; i++ {
				if online, err := s.tunnel.Status(); online || err != nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	online, err := s.tunnel.Status()
	host := s.creghtHost()
	out := map[string]any{"enabled": s.cfg.RemoteTunnel, "online": online, "available": creght.Has(host, "remote_tunnel"), "logged_in": hasToken(host)}
	var pe *tunnel.Error
	switch {
	case errors.As(err, &pe):
		out["error"], out["code"] = pe.Error(), pe.Code
		if pe.SubscribeURL != "" {
			out["subscribe_url"] = pe.SubscribeURL
		}
	case err != nil:
		out["error"] = err.Error()
	}
	if out["available"] == true && out["logged_in"] == true {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if sub, err := s.remoteSubscription(ctx, r.Method == http.MethodPut); err == nil {
			out["subscription"] = sub
		}
		if online {
			s.remoteURLs.refreshSoon(s)
		}
	}
	// 每个项目在手机上打开的地址（连上过隧道才有），当前项目排第一
	addrs := []map[string]any{}
	for pid, u := range s.remoteURLs.get() {
		ws, err := creght.OpenWorkspace(s.cfg.DirFor(pid))
		if err != nil {
			continue
		}
		addrs = append(addrs, map[string]any{"project_id": pid, "name": s.projectName(pid, ws.Dir), "url": strings.TrimRight(u, "/") + "/", "current": s.ready.Load() && pid == s.ws.ProjectID})
	}
	slices.SortFunc(addrs, func(a, b map[string]any) int {
		if a["current"] != b["current"] {
			if a["current"] == true {
				return -1
			}
			return 1
		}
		return strings.Compare(fmt.Sprint(a["name"]), fmt.Sprint(b["name"]))
	})
	out["projects"] = addrs
	writeJSON(w, out)
}
