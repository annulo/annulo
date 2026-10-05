// Package relay 让站点 Func 调到这台电脑上的本机函数（docs/mobile-remote.md「中转」）：
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
	// Run 执行一次调用：fn 形如 xhs.publish，emit 推进度；返回值要能转 JSON
	Run func(ctx context.Context, projectID, fn string, input any, emit func(data any)) (any, error)
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
	Fn          string              `json:"fn,omitempty"`
	Input       json.RawMessage     `json:"input,omitempty"`
	TimeoutMS   int64               `json:"timeout_ms,omitempty"`
	Data        any                 `json:"data,omitempty"`
	Value       any                 `json:"value,omitempty"`
	Message     string              `json:"message,omitempty"`
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
	wake    chan struct{} // 开关、项目变了：不用等退避，马上重来
	sent    string        // 上次 hello 报的项目和 remote 函数：变了就在同一条连接上重发
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

func (c *Client) setState(online bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !online && c.h.Enabled != nil && !c.h.Enabled() {
		c.online, c.lastErr = false, "" // 关掉开关断开的，不算出错
		return
	}
	if online && !c.online {
		log.Printf("中转：已连上平台")
	}
	c.online = online
	if err != nil {
		if msg := err.Error(); msg != c.lastErr {
			log.Printf("中转：连接断开：%s", msg)
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
		return errors.New("还没登录 creght")
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, connectURL(c.h.APIHost()), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
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
		c.mu.Unlock()
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
			log.Printf("中转：平台说消息不对：%s", m.Message)
		case "call":
			go c.call(sctx, m)
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
	m := message{Type: "hello", MachineID: c.h.MachineID, MachineName: c.h.MachineName, Projects: c.h.Projects()}
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
	projects := c.h.Projects()
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
