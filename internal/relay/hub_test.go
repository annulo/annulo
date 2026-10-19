package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 节点连上服务器的 Hub：报能力，服务器派一次能力调用拿到结果；令牌不对连不上；断开后调用报错
func TestHubNode(t *testing.T) {
	hub := NewHub(func(tok string) (string, bool) { return "n1", tok == "secret" })
	srv := httptest.NewServer(hub)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(Host{
		Name: "节点", URL: func() string { return wsURL }, Token: func() string { return "secret" },
		MachineID: "m1", MachineName: "mac-mini", Capabilities: []string{"browser"},
		Cap: func(ctx context.Context, call *Call) (any, error) {
			if call.Op == "fail" {
				return nil, errors.New("boom")
			}
			if call.Op == "ask" { // 回调服务器、推进度
				call.Progress(map[string]any{"step": 1})
				raw, err := call.Request(ctx, "double", map[string]any{"n": 21})
				if err != nil {
					return nil, err
				}
				var v float64
				json.Unmarshal(raw, &v)
				return map[string]any{"got": v}, nil
			}
			var in map[string]any
			json.Unmarshal(call.Args, &in)
			return map[string]any{"cap": call.Cap, "op": call.Op, "echo": in["x"]}, nil
		},
	})
	c.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(hub.Nodes()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	nodes := hub.Nodes()
	if len(nodes) != 1 || nodes[0].MachineName != "mac-mini" || nodes[0].Capabilities[0] != "browser" {
		t.Fatalf("节点没登记上：%+v", nodes)
	}
	id, err := hub.Pick("browser", "")
	if err != nil || id != "n1" {
		t.Fatal(id, err)
	}
	if _, err := hub.Pick("exec", ""); !errors.Is(err, ErrNoNode) {
		t.Fatal("没有这个能力的节点要报错")
	}
	raw, err := hub.Call(ctx, id, "browser", "open", map[string]any{"x": 42})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(raw, &out)
	if out["op"] != "open" || out["echo"].(float64) != 42 {
		t.Fatalf("结果不对：%s", raw)
	}
	var progressed json.RawMessage
	raw, err = hub.CallWith(ctx, id, "run", "ask", nil, CallOpts{
		Handle: func(ctx context.Context, op string, args json.RawMessage) (any, error) {
			var in struct{ N float64 }
			json.Unmarshal(args, &in)
			return in.N * 2, nil
		},
		Progress: func(d json.RawMessage) { progressed = d },
	})
	if err != nil || !strings.Contains(string(raw), `"got":42`) || !strings.Contains(string(progressed), `"step":1`) {
		t.Fatalf("回调服务器、推进度：%s %s %v", raw, progressed, err)
	}
	if _, err := hub.Call(ctx, id, "browser", "fail", nil); err == nil || err.Error() != "boom" {
		t.Fatalf("节点的错误要带回来：%v", err)
	}

	bad := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	hub.ServeHTTP(bad, r)
	if bad.Code != 401 {
		t.Fatal("令牌不对要 401")
	}

	cancel() // 节点断开
	deadline = time.Now().Add(5 * time.Second)
	for len(hub.Nodes()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := hub.Call(context.Background(), id, "browser", "open", nil); !errors.Is(err, ErrNoNode) {
		t.Fatalf("断开后要报没有节点：%v", err)
	}
}
