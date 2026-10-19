package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Hub 是服务端部署的 Annulo 这一头：用户电脑上的 Annulo 作为节点连进来（Client，Host.URL 指向这里），
// 服务器上的本机函数经 Call 借节点的能力（现在是浏览器）。协议和中转一样：hello → ready，call → result / error，cancel。
type Hub struct {
	// Auth 认节点令牌，返回令牌的 id（同一个令牌只给一台电脑用：新连上的顶掉旧的）
	Auth func(token string) (id string, ok bool)

	mu    sync.Mutex
	nodes map[string]*nodeConn // 令牌 id → 连接
	seq   atomic.Int64
}

// Node 是一个连着的节点。
type Node struct {
	ID           string    `json:"id"` // 令牌 id
	MachineID    string    `json:"machine_id"`
	MachineName  string    `json:"machine_name"`
	Capabilities []string  `json:"capabilities"`
	ConnectedAt  time.Time `json:"connected_at"`
}

type nodeConn struct {
	info    Node
	conn    *websocket.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[string]*pendingCall
}

type pendingCall struct {
	ch   chan hubReply
	opts CallOpts
}

// CallOpts：节点执行时回调服务器（Handle）、推进度（Progress）。都可以是 nil。
type CallOpts struct {
	Handle   func(ctx context.Context, op string, args json.RawMessage) (any, error)
	Progress func(data json.RawMessage)
}

type hubReply struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Message string          `json:"message,omitempty"`
	Call    string          `json:"call,omitempty"`
	Op      string          `json:"op,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var ErrNoNode = errors.New("没有连着的节点")

func NewHub(auth func(string) (string, bool)) *Hub {
	return &Hub{Auth: auth, nodes: map[string]*nodeConn{}}
}

// Nodes 是连着的节点，按连上的时间。
func (h *Hub) Nodes() []Node {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Node, 0, len(h.nodes))
	for _, n := range h.nodes {
		out = append(out, n.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

// Pick 选一个有这个能力的节点：prefer 是令牌 id，连着就用它；否则最早连上的那个。
func (h *Hub) Pick(capability, prefer string) (string, error) {
	nodes := h.Nodes()
	for _, n := range nodes {
		if n.ID == prefer && has(n.Capabilities, capability) {
			return n.ID, nil
		}
	}
	for _, n := range nodes {
		if has(n.Capabilities, capability) {
			return n.ID, nil
		}
	}
	return "", ErrNoNode
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Disconnect 断开某个令牌的连接（删令牌时）。
func (h *Hub) Disconnect(id string) {
	h.mu.Lock()
	n := h.nodes[id]
	h.mu.Unlock()
	if n != nil {
		n.conn.Close(websocket.StatusPolicyViolation, "token removed")
	}
}

// ServeHTTP 接受节点的连接：Authorization: Bearer <节点令牌>。
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, ok := h.Auth(tok)
	if tok == "" || !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 20) // 截图、网络响应可能很大
	defer conn.CloseNow()
	ctx := r.Context()

	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_, b, err := conn.Read(hctx)
	cancel()
	if err != nil {
		return
	}
	var hello message
	if json.Unmarshal(b, &hello) != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusPolicyViolation, "hello first")
		return
	}
	n := &nodeConn{conn: conn, pending: map[string]*pendingCall{}, info: Node{ID: id, MachineID: hello.MachineID, MachineName: hello.MachineName, Capabilities: hello.Capabilities, ConnectedAt: time.Now()}}
	h.mu.Lock()
	old := h.nodes[id]
	h.nodes[id] = n
	h.mu.Unlock()
	if old != nil {
		old.conn.Close(websocket.StatusPolicyViolation, "replaced by a new connection")
	}
	log.Printf("节点：%s（%s）连上了，能力 %v", n.info.MachineName, id, n.info.Capabilities)
	defer func() {
		h.mu.Lock()
		if h.nodes[id] == n {
			delete(h.nodes, id)
		}
		h.mu.Unlock()
		n.mu.Lock()
		for _, pc := range n.pending {
			close(pc.ch)
		}
		n.pending = map[string]*pendingCall{}
		n.mu.Unlock()
		log.Printf("节点：%s 断开了", n.info.MachineName)
	}()
	if err := n.write(ctx, map[string]any{"type": "ready"}); err != nil {
		return
	}
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m hubReply
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		switch m.Type {
		case "hello": // keepalive 里重发的：更新能力
			var again message
			if json.Unmarshal(b, &again) == nil {
				h.mu.Lock()
				n.info.Capabilities, n.info.MachineName = again.Capabilities, again.MachineName
				h.mu.Unlock()
			}
		case "result", "error":
			n.mu.Lock()
			pc := n.pending[m.ID]
			delete(n.pending, m.ID)
			n.mu.Unlock()
			if pc != nil {
				pc.ch <- m
				close(pc.ch)
			}
		case "progress":
			n.mu.Lock()
			pc := n.pending[m.ID]
			n.mu.Unlock()
			if pc != nil && pc.opts.Progress != nil {
				pc.opts.Progress(m.Data)
			}
		case "ctx": // 节点执行时回调服务器：交给这次调用的 Handle，回 ctx_result / ctx_error
			n.mu.Lock()
			pc := n.pending[m.Call]
			n.mu.Unlock()
			go func(m hubReply) {
				if pc == nil || pc.opts.Handle == nil {
					n.write(ctx, map[string]any{"type": "ctx_error", "id": m.ID, "message": "这次调用不接受回调"})
					return
				}
				v, err := pc.opts.Handle(ctx, m.Op, m.Args)
				if err != nil {
					n.write(ctx, map[string]any{"type": "ctx_error", "id": m.ID, "message": err.Error()})
					return
				}
				n.write(ctx, map[string]any{"type": "ctx_result", "id": m.ID, "value": v})
			}(m)
		}
	}
}

func (n *nodeConn) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	n.wmu.Lock()
	defer n.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return n.conn.Write(wctx, websocket.MessageText, b)
}

// Call 让节点做一次能力调用，等结果（ctx 结束就告诉节点取消）。返回节点给的 JSON。
func (h *Hub) Call(ctx context.Context, nodeID, capability, op string, args any) (json.RawMessage, error) {
	return h.CallWith(ctx, nodeID, capability, op, args, CallOpts{})
}

// CallWith 同 Call，节点执行时能回调服务器、推进度（服务器派函数给节点跑）。
func (h *Hub) CallWith(ctx context.Context, nodeID, capability, op string, args any, opts CallOpts) (json.RawMessage, error) {
	h.mu.Lock()
	n := h.nodes[nodeID]
	h.mu.Unlock()
	if n == nil {
		return nil, ErrNoNode
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("c%d", h.seq.Add(1))
	ch := make(chan hubReply, 1)
	n.mu.Lock()
	n.pending[id] = &pendingCall{ch: ch, opts: opts}
	n.mu.Unlock()
	timeout := int64(0)
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl).Milliseconds()
	}
	if err := n.write(ctx, map[string]any{"type": "call", "id": id, "cap": capability, "op": op, "args": json.RawMessage(raw), "timeout_ms": timeout}); err != nil {
		n.mu.Lock()
		delete(n.pending, id)
		n.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("节点 %s 断开了", n.info.MachineName)
		}
		if m.Type == "error" {
			return nil, errors.New(m.Message)
		}
		return m.Value, nil
	case <-ctx.Done():
		n.mu.Lock()
		delete(n.pending, id)
		n.mu.Unlock()
		_ = n.write(context.Background(), map[string]any{"type": "cancel", "id": id})
		return nil, ctx.Err()
	}
}
