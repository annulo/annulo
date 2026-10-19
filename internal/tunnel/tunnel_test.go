package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// 假平台：认 token，在隧道上开流、发一个 HTTP 请求，读回响应
func TestTunnelRoundTrip(t *testing.T) {
	sessCh := make(chan *yamux.Session, 1)
	gotHdr := make(chan http.Header, 1)
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/shuttle/tunnel" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		gotHdr <- r.Header.Clone()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(-1)
		nc := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
		cfg := yamux.DefaultConfig()
		cfg.LogOutput = io.Discard
		sess, err := yamux.Client(nc, cfg)
		if err != nil {
			return
		}
		sessCh <- sess
		<-sess.CloseChan()
	}))
	defer plat.Close()

	c := New(Host{
		APIHost:   func() string { return plat.URL },
		Token:     func() string { return "tok" },
		MachineID: "m1", MachineName: "Mac",
		Projects: func() []string { return []string{"p1", "p2"} },
	})
	go http.Serve(c.Listener(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		for i := 0; i < 3; i++ { // 分块推：SSE 要能一块块到
			fmt.Fprintf(w, "data: %d %s\n\n", i, r.Header.Get("X-Annulo-Project"))
			w.(http.Flusher).Flush()
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	h := <-gotHdr
	if h.Get("X-Annulo-Machine-Id") != "m1" || h.Get("X-Annulo-Projects") != "p1,p2" {
		t.Fatalf("握手要带机器和项目：%v", h)
	}
	var sess *yamux.Session
	select {
	case sess = <-sessCh:
	case <-time.After(5 * time.Second):
		t.Fatal("没连上")
	}
	for i := 0; i < 2; i++ { // 一条隧道上多个请求
		st, err := sess.Open()
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", "http://x.remote.creght.cn/_shuttle/api/agent/events", nil)
		req.Header.Set("X-Annulo-Project", "p1")
		if err := req.Write(st); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(st), req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		st.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), "data: 2 p1") {
			t.Fatalf("响应不对：%d %q", resp.StatusCode, b)
		}
	}
	if on, err := c.Status(); !on || err != nil {
		t.Fatalf("要显示已连上：%v %v", on, err)
	}
}

// 平台拒绝（没订阅）：带回错误码和原因
func TestTunnelRefused(t *testing.T) {
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"code":"subscription_required","message":"还没开通远程访问"}`)
	}))
	defer plat.Close()
	c := New(Host{APIHost: func() string { return plat.URL }, Token: func() string { return "tok" }})
	err := c.session(context.Background())
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "subscription_required" || pe.Message != "还没开通远程访问" || pe.Status != 402 {
		t.Fatalf("要带回平台的原因：%v", err)
	}
}

// 连上以后平台断开（流量用完、过期、被顶掉）：关闭帧 4001，reason 是错误码
func TestTunnelKicked(t *testing.T) {
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Annulo-Machine-Name"); got != "%E5%B0%8F%E7%BA%A2%E7%9A%84%20Mac" {
			t.Errorf("电脑名要按 URL 编码：%q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
		conn.Close(4001, "remote_traffic_exceeded")
	}))
	defer plat.Close()
	connected := make(chan struct{}, 1)
	c := New(Host{APIHost: func() string { return plat.URL }, Token: func() string { return "tok" }, MachineName: "小红的 Mac", OnConnect: func() { connected <- struct{}{} }})
	go func() {
		for {
			if _, err := c.Listener().Accept(); err != nil {
				return
			}
		}
	}()
	err := c.session(context.Background())
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "remote_traffic_exceeded" || !strings.Contains(pe.Message, "流量用完") {
		t.Fatalf("要带回断开的原因：%v", err)
	}
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("连上时要调 OnConnect")
	}
}

// 平台通用的错误格式：code 是数字，message 照样要带回来
func TestTunnelRefusedNumericCode(t *testing.T) {
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":403,"message":"this API is not available to OAuth tokens"}`)
	}))
	defer plat.Close()
	c := New(Host{APIHost: func() string { return plat.URL }, Token: func() string { return "tok" }})
	var pe *Error
	if err := c.session(context.Background()); !errors.As(err, &pe) || pe.Code != "" || pe.Message != "this API is not available to OAuth tokens" {
		t.Fatalf("%v", err)
	}
}

// 平台统一的拒绝格式：code 是 4021，细分原因在 reason（docs/billing.md「额度提示」）
func TestTunnelRefusedUnified(t *testing.T) {
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":4021,"quota":"tunnel","reason":"remote_subscription_expired","message":"远程访问的订阅过期了","subscribe_url":"https://creght.cn/annulo/subscribe?quota=tunnel"}`)
	}))
	defer plat.Close()
	c := New(Host{APIHost: func() string { return plat.URL }, Token: func() string { return "tok" }})
	var pe *Error
	if err := c.session(context.Background()); !errors.As(err, &pe) || pe.Code != "remote_subscription_expired" || pe.SubscribeURL == "" || pe.Message != "远程访问的订阅过期了" {
		t.Fatalf("%v %+v", err, pe)
	}
}
