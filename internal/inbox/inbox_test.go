package inbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"sources":[{"id":"a","type":"webhook"}]}`, "handler"},
		{`{"handler":"a.b","sources":[]}`, "sources"},
		{`{"handler":"a.b","sources":[{"id":"a","type":"mailbox"}]}`, "mailbox"},
		{`{"handler":"a.b","sources":[{"id":"a","type":"webhook"},{"id":"a","type":"webhook"}]}`, "重复"},
		{`{"handler":"a.b","sources":[{"id":"a","type":"webhook","auth":{"kind":"hmac"}}]}`, "header"},
		{`{"handler":"a.b","sources":[{"id":"a","type":"webhook"}],"notify":[{"when":"received","to":"https://x","text":"x"}]}`, "secret:"},
	} {
		_, err := Parse("x", "inboxes/x.json", []byte(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：要报 %q，实际 %v", c.body, c.want, err)
		}
	}
	d, err := Parse("leads", "inboxes/leads.json", []byte(`{"handler":"leads.receive","sources":[{"id":"form","type":"webhook","dedupe":{"field":"id"}}]}`))
	if err != nil || d.Source("form") == nil || d.Keep() != 7*24*time.Hour {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestWebhookHMACAndVerify(t *testing.T) {
	raw := json.RawMessage(`{"id":"gh","type":"webhook","auth":{"kind":"hmac","header":"X-Sig","prefix":"sha256=","secret":"secret:K"},"verify":{"echo_query":"hub.challenge"},"dedupe":{"header":"X-Id"}}`)
	env := Env{Secret: func(n string) (string, bool) { return "key", n == "K" }}
	body := []byte(`{"a":1}`)
	m := hmac.New(sha256.New, []byte("key"))
	m.Write(body)
	sig := "sha256=" + hex.EncodeToString(m.Sum(nil))

	r := httptest.NewRequest("POST", "/_inbox/t", nil)
	r.Header.Set("X-Sig", sig)
	r.Header.Set("X-Id", "evt-1")
	ev, resp, err := webhook{}.Handle(r, body, raw, env)
	if err != nil || resp != nil || ev.DedupeKey != "evt-1" {
		t.Fatalf("%+v %+v %v", ev, resp, err)
	}
	var p WebhookPayload
	json.Unmarshal(ev.Payload, &p)
	if p.Body != `{"a":1}` || p.JSON.(map[string]any)["a"] != 1.0 {
		t.Fatalf("payload：%+v", p)
	}

	r.Header.Set("X-Sig", "sha256=00")
	if _, _, err := (webhook{}).Handle(r, body, raw, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("签名不对要拒：%v", err)
	}

	g := httptest.NewRequest("GET", "/_inbox/t?hub.challenge=abc", nil)
	ev, resp, err = webhook{}.Handle(g, nil, raw, env)
	if err != nil || ev != nil || resp == nil || string(resp.Body) != "abc" {
		t.Fatalf("握手：%+v %+v %v", ev, resp, err)
	}
}

func TestWebhookForm(t *testing.T) {
	r := httptest.NewRequest("POST", "/_inbox/t", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ev, _, err := webhook{}.Handle(r, []byte("name=A&msg=hi"), json.RawMessage(`{"id":"f","type":"webhook"}`), Env{})
	if err != nil {
		t.Fatal(err)
	}
	var p WebhookPayload
	json.Unmarshal(ev.Payload, &p)
	if p.Form["name"] != "A" || !strings.HasPrefix(ev.DedupeKey, "sha256:") {
		t.Fatalf("%+v %s", p, ev.DedupeKey)
	}
}

func TestRenderNotify(t *testing.T) {
	ev := Event{Source: SourceRef{ID: "form"}, Payload: json.RawMessage(`{"json":{"name":"A","n":3,"tags":["x","y"]}}`)}
	b, _ := RenderNotify(NotifyDef{Text: "{{payload.json.name}} {{payload.json.n}} {{payload.json.tags.1}} {{nope}} {{source.id}}"}, ev)
	if string(b) != `{"text":"A 3 y  form"}` {
		t.Fatalf("%s", b)
	}
}

func TestFileStore(t *testing.T) {
	s := NewFileStore(t.TempDir())
	e := Event{Inbox: "social/c", DedupeKey: "k", Payload: json.RawMessage(`{}`), ReceivedAt: time.Now().Add(-10 * 24 * time.Hour)}
	if dup, err := s.Put(&e); dup || err != nil {
		t.Fatal(dup, err)
	}
	e2 := Event{Inbox: "social/c", DedupeKey: "k", Payload: json.RawMessage(`{}`)}
	if dup, _ := s.Put(&e2); !dup {
		t.Fatal("要去重")
	}
	if p, _ := s.Pending("social/c", 0); len(p) != 1 {
		t.Fatal(p)
	}
	s.Prune("social/c", 7*24*time.Hour)
	if l, _ := s.List("social/c", 0); len(l) != 1 {
		t.Fatal("没处理的不删", l)
	}
	e.Status = Done
	s.Update(e)
	s.Prune("social/c", 7*24*time.Hour)
	if l, _ := s.List("social/c", 0); len(l) != 0 {
		t.Fatal("处理完过期的要删", l)
	}
	s.SetCursor("social/c", "mail", []byte("42"))
	if c, _ := s.Cursor("social/c", "mail"); string(c) != "42" {
		t.Fatal(string(c))
	}
}
