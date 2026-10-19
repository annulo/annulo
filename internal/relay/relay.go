// Package relay 是「节点连接」：一台电脑主动连上一个对端，报上自己是谁、有哪些项目和能力，对端按能力派活
// （docs/server-and-multi-project.md「浏览器：节点」，中转和节点是同一套协议）。对端有两种：
//   - creght 平台（中转）：手机上调到这台电脑的本机函数；
//   - 服务端部署的 Annulo（节点）：服务器上的本机函数借这台电脑的浏览器（能力调用，Hub 在 hub.go）。
//
// 中转：让站点 Func 调到这台电脑上的本机函数（docs/mobile-remote.md「中转」）：
// 手机上打开运营后台点「发布」，站点 Func 调 ctx.shuttle.call，平台把调用转给用户在线的电脑，Shuttle 在这里执行、回结果。
//
// Shuttle 主动连平台（出站长连接，电脑不用公网地址），用用户的 creght 登录认证，一台电脑一条连接，断了自动重连。
// 只执行本机函数文件里 export const remote = [...] 列出的函数（Run 里检查），平台转来什么都不会多执行。
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Host 是 Shuttle 给中转提供的：连哪、用谁的身份、这台电脑上有哪些项目、怎么执行。
type Host struct {
	APIHost     func() string // https://creght.cn 这类，连接地址由它换成 wss://…/api/u/shuttle/connect
	Token       func() string // 用户的 creght 登录；空就先不连
	MachineID   string
	MachineName string
	Projects    func() []string // 这台电脑上能执行的项目
	// Remote 是每个项目里声明了 remote 的函数（文件.函数）：报给平台，手机页面据此决定按钮能不能点。可以是 nil
	Remote  func() map[string][]string
	Enabled func() bool // 用户在 设置 → 远程访问 里打开了没有；关着就不连
	// Run 执行一次调用：fn 形如 xhs.publish，emit 推进度；返回值要能转 JSON。节点可以是 nil
	Run func(ctx context.Context, projectID, fn string, input any, emit func(data any)) (any, error)
	// Inbox：平台说某个项目的收件箱代收有新事件了（docs/inbox.md），去拉。可以是 nil
	Inbox func(projectID string)

	// 节点（连服务端 Annulo）才有的：
	// URL 是连接地址（ws(s)://…/_annulo/api/nodes/connect）；给了就不按 APIHost 拼 creght 的地址
	URL func() string
	// Name 写在日志里分清是哪条连接（「中转」「节点」）
	Name string
	// Capabilities 是这台电脑能做的（browser…），Cap 执行一次能力调用：cap 是能力，op 是操作，args 是参数
	Capabilities []string
	Cap          func(ctx context.Context, call *Call) (any, error)
	// Closed 在连接断开时调（节点：关掉对端开的浏览器）
	Closed func()
}

// 平台和 Shuttle 之间的消息（一条 JSON 一帧）
type message struct {
	Type        string              `json:"type"`
	ID          string              `json:"id,omitempty"`
	MachineID   string              `json:"machine_id,omitempty"`
	MachineName string              `json:"machine_name,omitempty"`
	Projects    []string            `json:"projects,omitempty"`
	Remote      map[string][]string `json:"remote,omitempty"`
	ProjectID   string              `json:"project_id,omitempty"`
	Project     string              `json:"project,omitempty"` // inbox：哪个项目有新事件
	Fn          string              `json:"fn,omitempty"`
	Input       json.RawMessage     `json:"input,omitempty"`
	TimeoutMS   int64               `json:"timeout_ms,omitempty"`
	Data        any                 `json:"data,omitempty"`
	// 节点：能力和能力调用
	Capabilities []string        `json:"capabilities,omitempty"`
	Cap          string          `json:"cap,omitempty"`
	Op           string          `json:"op,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	Call         string          `json:"call,omitempty"` // ctx：节点在执行哪次能力调用时回调对端（ctx_result 按 ID 回）
	Value        any             `json:"value,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// 一次调用最多跑多久（平台没给 timeout_ms 时）：站点 Func 自己的上限是 300 秒
const defaultCallTimeout = 290 * time.Second

// Client 维护到平台的连接。Start 之后一直重连，直到 ctx 结束。
type Client struct {
	h Host

	mu      sync.Mutex
	conn    *websocket.Conn
	calls   map[string]context.CancelFunc
	online  bool
	lastErr string
	wake    chan struct{}           // 开关、项目变了：不用等退避，马上重来
	sent    string                  // 上次 hello 报的项目和 remote 函数：变了就在同一条连接上重发
	reqs    map[string]chan message // 节点回调对端（Call.Request）等着的回复
	reqSeq  int
}

// Call 是一次能力调用（节点上）。执行时可以回调对端（Request：读写表、取密钥、调模型…），推进度（Progress）。
type Call struct {
	ID, Cap, Op string
	Args        json.RawMessage
	c           *Client
	ctx         context.Context
}

// Request 回调对端，等它的回复。
func (k *Call) Request(ctx context.Context, op string, args any) (json.RawMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	c := k.c
	ch := make(chan message, 1)
	c.mu.Lock()
	c.reqSeq++
	id := fmt.Sprintf("q%d", c.reqSeq)
	if c.reqs == nil {
		c.reqs = map[string]chan message{}
	}
	c.reqs[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.reqs, id)
		c.mu.Unlock()
	}()
	if err := c.send(ctx, message{Type: "ctx", ID: id, Call: k.ID, Op: op, Args: raw}); err != nil {
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errors.New("和对端的连接断了")
		}
		if m.Type == "error" {
			return nil, errors.New(m.Message)
		}
		return json.Marshal(m.Value)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Progress 推一条进度给对端（对端转给页面）。
func (k *Call) Progress(data any) {
	_ = k.c.send(k.ctx, message{Type: "progress", ID: k.ID, Data: data})
}

func New(h Host) *Client {
	return &Client{h: h, calls: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1)}
}

// Status 给设置页看：连上了没有，没连上是为什么。
func (c *Client) Status() (online bool, lastErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.online, c.lastErr
}

// Reconnect 断开重连：开关、打开的项目、所在的 creght 集群换了，重新连（或者断开不连），重新告诉平台这台电脑上有哪些项目。
func (c *Client) Reconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "workspace changed")
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Start 在后台连平台，断了按 1s、2s、4s… 最长 60s 重连。
func (c *Client) Start(ctx context.Context) {
	go func() {
		wait := time.Second
		for ctx.Err() == nil {
			if c.h.Enabled != nil && !c.h.Enabled() {
				c.setState(false, nil)
				select { // 关着：等用户打开开关
				case <-c.wake:
				case <-ctx.Done():
				}
				wait = time.Second
				continue
			}
			start := time.Now()
			err := c.session(ctx)
			c.setState(false, err)
			if ctx.Err() != nil {
				return
			}
			if time.Since(start) > time.Minute {
				wait = time.Second // 连了一阵才断的，从头开始退避
			}
			select {
			case <-time.After(wait):
			case <-c.wake:
			case <-ctx.Done():
				return
			}
			wait = min(wait*2, time.Minute)
		}
	}()
}

func (c *Client) name() string {
	if c.h.Name != "" {
		return c.h.Name
	}
	return "中转"
}

func (c *Client) setState(online bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !online && c.h.Enabled != nil && !c.h.Enabled() {
		c.online, c.lastErr = false, "" // 关掉开关断开的，不算出错
		return
	}
	if online && !c.online {
		log.Printf("%s：已连上", c.name())
	}
	c.online = online
	if err != nil {
		if msg := err.Error(); msg != c.lastErr {
			log.Printf("%s：连接断开：%s", c.name(), msg)
			c.lastErr = msg
		}
	} else if online {
		c.lastErr = ""
	}
}

// connectURL 把 https://creght.cn 换成 wss://creght.cn/api/u/shuttle/connect
func connectURL(apiHost string) string {
	u := strings.TrimRight(apiHost, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/api/u/shuttle/connect"
}

// session 连一次，直到断开。
func (c *Client) session(ctx context.Context) error {
	token := c.h.Token()
	if token == "" {
		if c.h.URL != nil {
			return errors.New("还没填节点令牌")
		}
		return errors.New("还没登录 creght")
	}
	target := ""
	if c.h.URL != nil {
		target = c.h.URL()
	} else {
		target = connectURL(c.h.APIHost())
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, target, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, "X-Annulo": {"1"}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			if c.h.URL != nil {
				return errors.New("服务器不认这个节点令牌：在服务器的 设置 → 节点 里重新生成一个")
			}
			return errors.New("creght 登录过期了：在 Annulo 里重新登录")
		}
		if resp != nil {
			return fmt.Errorf("连接被拒绝（HTTP %d）：%w", resp.StatusCode, err)
		}
		return err
	}
	conn.SetReadLimit(32 << 20) // 一次调用的 input 可能带长正文
	defer conn.CloseNow()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		// 连接断了，正在跑的调用平台那边已经报错了：停掉，别白跑（发布这类有副作用的也不该在没人等的时候继续）
		for _, stop := range c.calls {
			stop()
		}
		c.calls = map[string]context.CancelFunc{}
		for _, ch := range c.reqs {
			close(ch)
		}
		c.reqs = map[string]chan message{}
		c.mu.Unlock()
		if c.h.Closed != nil {
			c.h.Closed()
		}
	}()

	if err := c.hello(ctx); err != nil {
		return err
	}

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go c.keepalive(sctx, conn)

	for {
		_, b, err := conn.Read(sctx)
		if err != nil {
			return err
		}
		var m message
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		switch m.Type {
		case "ready": // 平台收下了 hello，这台电脑算在线了
			c.setState(true, nil)
		case "invalid":
			log.Printf("%s：对端说消息不对：%s", c.name(), m.Message)
		case "call":
			go c.call(sctx, m)
		case "ctx_result", "ctx_error": // 节点回调对端的回复
			c.mu.Lock()
			ch := c.reqs[m.ID]
			c.mu.Unlock()
			if ch != nil {
				if m.Type == "ctx_error" {
					m.Type = "error"
				}
				ch <- m
			}
		case "inbox": // 收件箱代收有新事件（尽力而为，拉取另有轮询兜底）
			if c.h.Inbox != nil && m.Project != "" {
				go c.h.Inbox(m.Project)
			}
		case "cancel":
			c.mu.Lock()
			if stop := c.calls[m.ID]; stop != nil {
				stop()
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) hello(ctx context.Context) error {
	m := message{Type: "hello", MachineID: c.h.MachineID, MachineName: c.h.MachineName, Capabilities: c.h.Capabilities}
	if c.h.Projects != nil {
		m.Projects = c.h.Projects()
	}
	if c.h.Remote != nil {
		m.Remote = c.h.Remote()
	}
	sig, _ := json.Marshal([]any{m.Projects, m.Remote})
	if err := c.send(ctx, m); err != nil {
		return err
	}
	c.mu.Lock()
	c.sent = string(sig)
	c.mu.Unlock()
	return nil
}

// rehello：项目或 remote 函数变了（助手新写了个发布脚本），重发 hello，手机上的按钮不用等重连就能亮
func (c *Client) rehello(ctx context.Context) error {
	var projects []string
	if c.h.Projects != nil {
		projects = c.h.Projects()
	}
	var remote map[string][]string
	if c.h.Remote != nil {
		remote = c.h.Remote()
	}
	sig, _ := json.Marshal([]any{projects, remote})
	c.mu.Lock()
	same := string(sig) == c.sent
	c.mu.Unlock()
	if same {
		return nil
	}
	return c.hello(ctx)
}

// keepalive 每 30 秒 ping 一次（中间的代理会掐掉长时间没动静的连接），顺便看项目和 remote 函数变了没有。
func (c *Client) keepalive(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.rehello(ctx); err != nil {
				conn.Close(websocket.StatusGoingAway, "hello failed")
				return
			}
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				conn.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func (c *Client) send(ctx context.Context, m message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("没有连接")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func (c *Client) call(ctx context.Context, m message) {
	timeout := defaultCallTimeout
	if m.TimeoutMS > 0 {
		timeout = time.Duration(m.TimeoutMS) * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c.mu.Lock()
	c.calls[m.ID] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.calls, m.ID)
		c.mu.Unlock()
	}()

	if m.Cap != "" { // 能力调用（节点）：服务器上的本机函数借这台电脑的浏览器
		if c.h.Cap == nil {
			_ = c.send(ctx, message{Type: "error", ID: m.ID, Message: "这台电脑没有这个能力：" + m.Cap})
			return
		}
		res, err := c.h.Cap(cctx, &Call{ID: m.ID, Cap: m.Cap, Op: m.Op, Args: m.Args, c: c, ctx: ctx})
		if err != nil {
			_ = c.send(ctx, message{Type: "error", ID: m.ID, Message: err.Error()})
			return
		}
		_ = c.send(ctx, message{Type: "result", ID: m.ID, Value: res})
		return
	}
	if c.h.Run == nil {
		_ = c.send(ctx, message{Type: "error", ID: m.ID, Message: "这条连接不执行本机函数"})
		return
	}
	var input any
	if len(m.Input) > 0 {
		_ = json.Unmarshal(m.Input, &input)
	}
	start := time.Now()
	res, err := c.h.Run(cctx, m.ProjectID, m.Fn, input, func(data any) {
		_ = c.send(ctx, message{Type: "progress", ID: m.ID, Data: data})
	})
	if err != nil {
		log.Printf("中转：%s 失败（%dms）：%v", m.Fn, time.Since(start).Milliseconds(), err)
		_ = c.send(ctx, message{Type: "error", ID: m.ID, Message: err.Error()})
		return
	}
	log.Printf("中转：%s 完成（%dms）", m.Fn, time.Since(start).Milliseconds())
	_ = c.send(ctx, message{Type: "result", ID: m.ID, Value: res})
}
