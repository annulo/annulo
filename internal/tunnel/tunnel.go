// Package tunnel 是远程访问的隧道（docs/mobile-remote.md「远程访问：隧道」）：
// 手机打开 https://<随机串>.remote.creght.cn，creght 把请求经这条隧道转给电脑上的 Annulo，手机上用的就是 Annulo 本身。
//
// Annulo 主动连平台（出站 WebSocket，电脑不用公网地址），连接上跑 yamux：平台开流，一条流一个 HTTP/1.1 请求，
// 这里把流当成连接交给 Listener，Annulo 用 http.Serve 接住，分块、SSE 和本机一样。
// 隧道那头只有 Annulo 自己的 handler，平台选不了这台电脑上的端口。
//
// 和中转（internal/relay）是两条连接：中转照旧给在线项目调本机函数，隧道转整个 Annulo。
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Host 是 Annulo 给隧道提供的：连哪、用谁的身份、这台电脑是谁、开没开。
type Host struct {
	APIHost     func() string // https://creght.cn 这类；空就先不连（没连 creght、这个 creght 没有远程访问）
	Token       func() string // 用户的 creght 登录；空就先不连
	MachineID   string
	MachineName string
	Projects    func() []string // 这台电脑上能远程打开的项目（握手时报给平台，平台据此分配地址）
	Enabled     func() bool     // 设置 → 远程访问 里打开了没有
	OnConnect   func()          // 连上以后（平台这时已经建好各项目的地址，Annulo 去查一次）
}

// Error 是平台拒绝建隧道、或者连上以后把隧道断开时说的原因（没订阅、过期、电脑数超了、流量用完…），界面按 Code 放开通按钮。
// 错误码见 weave docs/architecture/remote-tunnel.md「订阅」。
type Error struct {
	Status       int
	Code         string
	Message      string
	SubscribeURL string // 开通页
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("平台拒绝了连接（%d %s）", e.Status, e.Code)
}

// Client 维护到平台的隧道。Start 之后一直重连；Listener 在重连之间不变，http.Serve 只调一次。
type Client struct {
	h  Host
	ln *listener

	mu      sync.Mutex
	sess    *yamux.Session
	online  bool
	lastErr error
	wake    chan struct{}
}

func New(h Host) *Client {
	return &Client{h: h, ln: newListener(), wake: make(chan struct{}, 1)}
}

// Listener 是平台转来的请求：每个 Accept 是一条 yamux 流。
func (c *Client) Listener() net.Listener { return c.ln }

// Status 给设置页看：连上了没有，没连上是为什么（*Error 是平台拒绝的原因）。
func (c *Client) Status() (online bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.online, c.lastErr
}

// Reconnect 断开重连：开关、项目、creght 集群变了。
func (c *Client) Reconnect() {
	c.mu.Lock()
	sess := c.sess
	c.lastErr = nil // 上次的原因不算数了：界面等这次重连的结果
	c.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Start 在后台连平台，断了按 1s、2s、4s… 最长 60s 重连；平台拒绝（没订阅这类）时最长 10 分钟再试，改了设置马上重试。
func (c *Client) Start(ctx context.Context) {
	go func() {
		wait := time.Second
		for ctx.Err() == nil {
			if !c.enabled() {
				c.setState(false, nil)
				select {
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
				wait = time.Second
			}
			limit := time.Minute
			var pe *Error
			if errors.As(err, &pe) {
				limit = 10 * time.Minute
			}
			select {
			case <-time.After(wait):
			case <-c.wake:
				wait = time.Second
				continue
			case <-ctx.Done():
				return
			}
			wait = min(wait*2, limit)
		}
	}()
}

func (c *Client) enabled() bool {
	return (c.h.Enabled == nil || c.h.Enabled()) && c.h.APIHost() != ""
}

func (c *Client) setState(online bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !online && !c.enabled() {
		c.online, c.lastErr = false, nil
		return
	}
	if online && !c.online {
		log.Printf("隧道：已连上")
	}
	c.online = online
	if err != nil {
		if c.lastErr == nil || err.Error() != c.lastErr.Error() {
			log.Printf("隧道：连接断开：%v", err)
		}
		c.lastErr = err
	} else if online {
		c.lastErr = nil
	}
}

// connectURL 把 https://creght.cn 换成 wss://creght.cn/api/u/shuttle/tunnel
func connectURL(apiHost string) string {
	u := strings.TrimRight(apiHost, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/api/u/shuttle/tunnel"
}

// session 连一次，直到断开。
func (c *Client) session(ctx context.Context) error {
	token := c.h.Token()
	if token == "" {
		return errors.New("还没登录 creght")
	}
	hdr := http.Header{
		"Authorization":         {"Bearer " + token},
		"X-Annulo":              {"1"},
		"X-Annulo-Machine-Id":   {c.h.MachineID},
		"X-Annulo-Machine-Name": {url.PathEscape(c.h.MachineName)}, // 电脑名可能是中文，平台按 URL 编码解
	}
	if c.h.Projects != nil {
		hdr.Set("X-Annulo-Projects", strings.Join(c.h.Projects(), ","))
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, connectURL(c.h.APIHost()), &websocket.DialOptions{HTTPHeader: hdr})
	cancel()
	if err != nil {
		if pe := refusal(resp); pe != nil {
			return pe
		}
		return err
	}
	conn.SetReadLimit(-1) // 流量由 yamux 自己分帧，上传的大文件也走这里
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	nc := &closeConn{Conn: websocket.NetConn(sctx, conn, websocket.MessageBinary)}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 25 * time.Second
	sess, err := yamux.Server(nc, cfg)
	if err != nil {
		nc.Close()
		return err
	}
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.sess == sess {
			c.sess = nil
		}
		c.mu.Unlock()
		sess.Close()
	}()
	c.setState(true, nil)
	if c.h.OnConnect != nil {
		go c.h.OnConnect()
	}
	for {
		st, err := sess.Accept()
		if err != nil {
			if pe := nc.kicked(); pe != nil {
				return pe
			}
			if sess.IsClosed() {
				return errors.New("连接断开")
			}
			return err
		}
		if !c.ln.push(st) {
			st.Close()
			return net.ErrClosed
		}
	}
}

// refusal 读平台拒绝建隧道的原因，读不到就按状态码说。平台统一的格式（docs/billing.md「额度提示」）是
// { "code": 4021, "quota": "tunnel", "reason": "remote_subscription_expired", "message", "subscribe_url" }；
// 老的格式 code 本身是字符串（remote_…），也认。
func refusal(resp *http.Response) *Error {
	if resp == nil || resp.StatusCode < 400 {
		return nil
	}
	e := &Error{Status: resp.StatusCode}
	if resp.Body != nil {
		var body struct {
			Code         any    `json:"code"`   // 老格式是字符串（remote_…）；统一格式是数字 4021，细分原因在 reason
			Reason       string `json:"reason"` // remote_subscription_required…
			Message      string `json:"message"`
			Msg          string `json:"msg"`
			SubscribeURL string `json:"subscribe_url"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &body) == nil {
			e.Message, e.SubscribeURL = body.Message, body.SubscribeURL
			if c, ok := body.Code.(string); ok {
				e.Code = c
			}
			if body.Reason != "" {
				e.Code = body.Reason
			}
			if e.Message == "" {
				e.Message = body.Msg
			}
		}
	}
	if e.Code == "" && resp.StatusCode == http.StatusUnauthorized {
		e.Code = "unauthorized"
	}
	if e.Message == "" && resp.StatusCode == http.StatusUnauthorized {
		e.Message = "creght 的授权失效了：到 设置 → 连接 里重新连接 creght"
	}
	return e
}

// kickCode 是平台断开隧道时关闭帧的状态码，reason 是错误码（过期、流量用完、被同一台电脑的新连接顶掉）。
const kickCode websocket.StatusCode = 4001

// kickMessages：关闭帧里只有错误码，原因在这里写。
var kickMessages = map[string]string{
	"remote_subscription_required": "远程访问还没开通，或者已经关闭了",
	"remote_subscription_expired":  "远程访问的订阅过期了",
	"remote_traffic_exceeded":      "这个月远程访问的流量用完了",
	"remote_machine_limit":         "能同时远程访问的电脑数已经满了",
	"remote_replaced":              "这台电脑在别处又连了一次（同一个数据目录开了两个 Annulo？），这条连接被顶掉了",
}

// closeConn 记下读到的第一个错误：平台用关闭帧 4001 说明为什么断开。
type closeConn struct {
	net.Conn
	mu  sync.Mutex
	err error
}

func (c *closeConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	return n, err
}

// kicked：平台主动断开时返回原因；别的断开返回 nil。
func (c *closeConn) kicked() *Error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != kickCode {
		return nil
	}
	msg := kickMessages[ce.Reason]
	if msg == "" {
		msg = "平台断开了连接：" + ce.Reason
	}
	return &Error{Code: ce.Reason, Message: msg}
}

// listener 把各次连接里平台开的流交给 http.Serve。
type listener struct {
	ch     chan net.Conn
	done   chan struct{}
	closed sync.Once
}

func newListener() *listener {
	return &listener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *listener) push(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		return false
	}
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *listener) Addr() net.Addr { return addr{} }

type addr struct{}

func (addr) Network() string { return "tunnel" }
func (addr) String() string  { return "tunnel" }
