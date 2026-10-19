package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/localfn"
)

type memDB map[string][]map[string]any

func (m memDB) Query(_ context.Context, t string, q localfn.Query) ([]map[string]any, string, int64, error) {
	return m[t], "", int64(len(m[t])), nil
}
func (m memDB) Aggregate(context.Context, string, map[string]any, []localfn.Cond) ([]map[string]any, bool, error) {
	return nil, false, nil
}
func (m memDB) Get(_ context.Context, t, id string) (map[string]any, error) { return nil, nil }
func (m memDB) Insert(_ context.Context, t string, d map[string]any) (map[string]any, error) {
	d["id"] = "r1"
	m[t] = append(m[t], d)
	return d, nil
}
func (m memDB) Update(context.Context, string, string, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (m memDB) Delete(context.Context, string, string) error { return nil }

// 节点上的 ctx.db / 密钥回调服务器：nodeDB 发出去的参数，nodeCallback 认得
func TestNodeCallbackRoundTrip(t *testing.T) {
	db := memDB{}
	h := localfn.Host{DB: db, Secret: func(n string) (string, bool) { return "v-" + n, true }}
	req := func(op string, args any, out any) error {
		raw, _ := json.Marshal(args)
		v, err := nodeCallback(context.Background(), h, op, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		if out != nil {
			return json.Unmarshal(b, out)
		}
		return nil
	}
	nd := nodeDB{req}
	row, err := nd.Insert(context.Background(), "notes", map[string]any{"text": "hi"})
	if err != nil || row["id"] != "r1" {
		t.Fatal(row, err)
	}
	list, _, total, err := nd.Query(context.Background(), "notes", localfn.Query{Limit: 10, Where: map[string]any{"a": 1}})
	if err != nil || total != 1 || list[0]["text"] != "hi" {
		t.Fatal(list, total, err)
	}
	var sec struct {
		Value string
		OK    bool `json:"ok"`
	}
	if err := req("secret", map[string]any{"name": "KEY"}, &sec); err != nil || sec.Value != "v-KEY" || !sec.OK {
		t.Fatal(sec, err)
	}
	if _, err := nodeCallback(context.Background(), h, "llm", nil); err == nil {
		t.Fatal("服务器没有的能力要报错")
	}
}

// 节点上 b.upload 用到 local:<name>：分段从服务器取回来，比一段大的文件也取全；名字不对的不给
func TestNodeLocalFile(t *testing.T) {
	files := t.TempDir()
	name := "0123456789abcdef0123456789abcdef.mp4"
	want := bytes.Repeat([]byte("annulo"), nodeFileChunk/6+100) // 比一段多一点
	os.WriteFile(filepath.Join(files, name), want, 0o600)
	h := localfn.Host{FilesDir: files}
	calls := 0
	req := func(op string, args any, out any) error {
		calls++
		raw, _ := json.Marshal(args)
		v, err := nodeCallback(context.Background(), h, op, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	dst := filepath.Join(t.TempDir(), "x.mp4")
	if err := nodeLocalFile(req)(context.Background(), name, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) || calls != 2 {
		t.Fatalf("取回来的不对：%d 字节（要 %d），调了 %d 次", len(got), len(want), calls)
	}
	for _, bad := range []string{"../x.mp4", "nope.mp4"} {
		if err := nodeLocalFile(req)(context.Background(), bad, dst); err == nil {
			t.Fatalf("%s 应该报错", bad)
		}
	}
}
