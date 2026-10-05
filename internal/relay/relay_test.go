package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 假平台：收 hello，发两次调用（一次成功、一次出错），收进度和结果，然后断开，看客户端重连
func TestClientCalls(t *testing.T) {
	got := make(chan message, 20)
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/shuttle/connect" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		conns++
		ctx := r.Context()
		read := func() message {
			_, b, err := c.Read(ctx)
			if err != nil {
				return message{Type: "closed"}
			}
			var m message
			json.Unmarshal(b, &m)
			return m
		}
		got <- read() // hello
		c.Write(ctx, websocket.MessageText, []byte(`{"type":"ready","conn_id":"x"}`))
		if conns > 1 {
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		for _, call := range []string{
			`{"type":"call","id":"c1","project_id":"p1","fn":"xhs.publish","input":{"post_id":"a"}}`,
			`{"type":"call","id":"c2","project_id":"p1","fn":"xhs.bad","input":{}}`,
		} {
			c.Write(ctx, websocket.MessageText, []byte(call))
		}
		for i := 0; i < 3; i++ { // c1 的进度 + 结果，c2 的错误
			got <- read()
		}
		c.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer srv.Close()

	cl := New(Host{
		APIHost:     func() string { return srv.URL },
		Token:       func() string { return "tok" },
		MachineID:   "m1",
		MachineName: "测试机",
		Projects:    func() []string { return []string{"p1"} },
		Remote:      func() map[string][]string { return map[string][]string{"p1": {"xhs.publish"}} },
		Run: func(ctx context.Context, projectID, fn string, input any, emit func(any)) (any, error) {
			if fn == "xhs.bad" {
				return nil, errors.New("没登录小红书")
			}
			emit(map[string]any{"message": "上传中"})
			return map[string]any{"url": "https://x/1", "input": input}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl.Start(ctx)

	next := func() message {
		select {
		case m := <-got:
			return m
		case <-time.After(10 * time.Second):
			t.Fatal("等不到消息")
		}
		return message{}
	}
	if m := next(); m.Type != "hello" || m.MachineID != "m1" || len(m.Projects) != 1 || m.Projects[0] != "p1" || len(m.Remote["p1"]) != 1 || m.Remote["p1"][0] != "xhs.publish" {
		t.Fatalf("hello 不对：%+v", m)
	}
	seen := map[string]message{}
	for i := 0; i < 3; i++ {
		m := next()
		seen[m.Type+":"+m.ID] = m
	}
	if _, ok := seen["progress:c1"]; !ok {
		t.Fatalf("没有进度：%v", seen)
	}
	if r, ok := seen["result:c1"]; !ok || r.Value.(map[string]any)["url"] != "https://x/1" {
		t.Fatalf("结果不对：%v", seen)
	}
	if e, ok := seen["error:c2"]; !ok || e.Message != "没登录小红书" {
		t.Fatalf("错误不对：%v", seen)
	}
	// 平台断开后应该重连（第二次 hello）
	if m := next(); m.Type != "hello" {
		t.Fatalf("断开后没重连：%+v", m)
	}
}

func TestConnectURL(t *testing.T) {
	for in, want := range map[string]string{"https://creght.cn": "wss://creght.cn/api/u/shuttle/connect", "http://localhost:8080/": "ws://localhost:8080/api/u/shuttle/connect"} {
		if got := connectURL(in); got != want {
			t.Errorf("%s → %s，要 %s", in, got, want)
		}
	}
}
