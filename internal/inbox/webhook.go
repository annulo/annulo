package inbox

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/annulo/annulo/internal/i18n"
)

// webhook：别人调收件箱的地址（/_inbox/<令牌>），原样存整个请求，不解析业务内容。
//
//	{ "id": "site_form", "type": "webhook" }
//	{ "id": "github", "type": "webhook",
//	  "auth": { "kind": "hmac", "header": "X-Hub-Signature-256", "algo": "sha256", "prefix": "sha256=", "secret": "secret:github_hook" },
//	  "dedupe": { "header": "X-GitHub-Delivery" } }
//	{ "id": "fb", "type": "webhook", "verify": { "echo_query": "hub.challenge" } }
//
// 地址里的令牌总是要有（url_token）；hmac 在它之上再验签名。写不出来的签名、握手在 handler 里自己校验（原始请求都在）。
type webhook struct{}

func init() { Register(webhook{}) }

type webhookCfg struct {
	Auth struct {
		Kind     string `json:"kind"`     // url_token（默认）| hmac
		Header   string `json:"header"`   // 签名在哪个请求头
		Algo     string `json:"algo"`     // sha256（默认）| sha1 | sha512
		Prefix   string `json:"prefix"`   // 签名前面的固定串，比如 sha256=
		Encoding string `json:"encoding"` // hex（默认）| base64
		Secret   string `json:"secret"`   // secret:<名字>
	} `json:"auth"`
	Verify struct {
		EchoQuery string `json:"echo_query"` // GET 带这个参数时原样回它（握手）
	} `json:"verify"`
	Dedupe struct {
		Header string `json:"header"` // 平台给的事件 id 在哪个请求头
		Field  string `json:"field"`  // 或者 JSON body 里的哪个字段（a.b）
	} `json:"dedupe"`
}

// WebhookPayload 是 webhook 来源的 payload。
type WebhookPayload struct {
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	Query        map[string]string `json:"query"`
	ContentType  string            `json:"content_type,omitempty"`
	Body         string            `json:"body"`
	BodyEncoding string            `json:"body_encoding,omitempty"` // 不是文本时 base64
	JSON         any               `json:"json,omitempty"`          // body 能按 JSON 解析时
	Form         map[string]string `json:"form,omitempty"`          // 表单提交（x-www-form-urlencoded）时
}

func (webhook) Type() string { return "webhook" }
func (webhook) Mode() Mode   { return Push }

// 本机版也收，但只有这台电脑上的程序调得到；外面的要等云端代收（RunCloud，没做）
func (webhook) Runners() []Runner { return []Runner{RunLocal, RunServer, RunCloud} }

func parseWebhook(raw json.RawMessage) (webhookCfg, error) {
	var c webhookCfg
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	return c, nil
}

func (webhook) Validate(raw json.RawMessage) error {
	c, err := parseWebhook(raw)
	if err != nil {
		return err
	}
	switch c.Auth.Kind {
	case "", "url_token":
	case "hmac":
		if c.Auth.Header == "" {
			return i18n.New("hmac 要写 header（签名在哪个请求头）", "hmac needs header (which request header has the signature)")
		}
		if !strings.HasPrefix(c.Auth.Secret, "secret:") {
			return i18n.New("hmac 的 secret 要写 secret:<密钥名>", "hmac secret must be secret:<name>")
		}
		if hashOf(c.Auth.Algo) == nil {
			return i18n.Errorf("不认识的 algo %q（sha256、sha1、sha512）", "Unknown algo %q (sha256, sha1, sha512)", c.Auth.Algo)
		}
		if e := c.Auth.Encoding; e != "" && e != "hex" && e != "base64" {
			return i18n.Errorf("不认识的 encoding %q（hex、base64）", "Unknown encoding %q (hex, base64)", e)
		}
	default:
		return i18n.Errorf("不认识的 auth.kind %q（url_token、hmac）", "Unknown auth.kind %q (url_token, hmac)", c.Auth.Kind)
	}
	if c.Dedupe.Header != "" && c.Dedupe.Field != "" {
		return i18n.New("dedupe 的 header、field 只写一个", "dedupe: use only one of header and field")
	}
	return nil
}

func hashOf(algo string) func() hash.Hash {
	switch algo {
	case "", "sha256":
		return sha256.New
	case "sha1":
		return sha1.New
	case "sha512":
		return sha512.New
	}
	return nil
}

func (webhook) Handle(r *http.Request, body []byte, raw json.RawMessage, env Env) (*Event, *Response, error) {
	c, err := parseWebhook(raw)
	if err != nil {
		return nil, nil, err
	}
	if q := c.Verify.EchoQuery; q != "" && r.Method == http.MethodGet && r.URL.Query().Has(q) {
		return nil, &Response{Status: http.StatusOK, ContentType: "text/plain; charset=utf-8", Body: []byte(r.URL.Query().Get(q))}, nil
	}
	if c.Auth.Kind == "hmac" {
		if err := checkHMAC(c, r.Header.Get(c.Auth.Header), body, env); err != nil {
			return nil, nil, err
		}
	}
	p := WebhookPayload{Method: r.Method, Headers: map[string]string{}, Query: map[string]string{}, ContentType: r.Header.Get("Content-Type")}
	for k, v := range r.Header {
		p.Headers[k] = strings.Join(v, ", ")
	}
	for k, v := range r.URL.Query() {
		p.Query[k] = strings.Join(v, ", ")
	}
	if utf8.Valid(body) {
		p.Body = string(body)
	} else {
		p.Body, p.BodyEncoding = base64.StdEncoding.EncodeToString(body), "base64"
	}
	var j any
	if len(body) > 0 && json.Unmarshal(body, &j) == nil {
		p.JSON = j
	}
	if strings.HasPrefix(p.ContentType, "application/x-www-form-urlencoded") {
		if v, err := url.ParseQuery(string(body)); err == nil {
			p.Form = map[string]string{}
			for k, vs := range v {
				p.Form[k] = strings.Join(vs, ", ")
			}
		}
	}
	key := ""
	switch {
	case c.Dedupe.Header != "":
		key = r.Header.Get(c.Dedupe.Header)
	case c.Dedupe.Field != "" && p.JSON != nil:
		if v, ok := Lookup(p.JSON, c.Dedupe.Field); ok {
			key = stringify(v)
		}
	}
	if key == "" { // 默认：同样的请求（方法、参数、内容）只算一条，平台重试不会处理两遍
		h := sha256.Sum256([]byte(r.Method + "\x00" + r.URL.RawQuery + "\x00" + string(body)))
		key = "sha256:" + hex.EncodeToString(h[:])
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, nil, err
	}
	return &Event{DedupeKey: key, Payload: b}, nil, nil
}

func checkHMAC(c webhookCfg, got string, body []byte, env Env) error {
	name := strings.TrimPrefix(c.Auth.Secret, "secret:")
	key, ok := "", false
	if env.Secret != nil {
		key, ok = env.Secret(name)
	}
	if !ok || key == "" {
		return i18n.Errorf("没有密钥 %s：到 设置 → 密钥 里加上", "Secret %s is missing: add it in Settings → Secrets", name)
	}
	got = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(got), c.Auth.Prefix))
	if got == "" {
		return ErrUnauthorized
	}
	m := hmac.New(hashOf(c.Auth.Algo), []byte(key))
	m.Write(body)
	sum := m.Sum(nil)
	var want []byte
	if c.Auth.Encoding == "base64" {
		want = []byte(base64.StdEncoding.EncodeToString(sum))
	} else {
		want = []byte(hex.EncodeToString(sum))
		got = strings.ToLower(got)
	}
	if !hmac.Equal(want, []byte(got)) {
		return ErrUnauthorized
	}
	return nil
}

// TestEvent 造一条测试事件（管理页「发一条测试」），走和真的一样的处理和通知。
func TestEvent(sourceType string) (json.RawMessage, error) {
	switch sourceType {
	case "webhook":
		body := `{"test":true,"name":"Test","email":"test@example.com","message":"This is a test event from Annulo."}`
		var j any
		json.Unmarshal([]byte(body), &j)
		return json.Marshal(WebhookPayload{Method: "POST", Headers: map[string]string{"Content-Type": "application/json"}, Query: map[string]string{}, ContentType: "application/json", Body: body, JSON: j})
	}
	return nil, i18n.Errorf("来源类型 %s 还不能发测试", "Source type %s can't send a test yet", sourceType)
}

// CheckRelayed 处理云端代收拉下来的一条 webhook（云端只认令牌、不验签名，密钥不上云）：
// 配了 hmac 的在这里验，配了按 header / 字段去重的算出 dedupe_key（没配返回空，用云端给的）。
func CheckRelayed(raw json.RawMessage, payload json.RawMessage, env Env) (string, error) {
	c, err := parseWebhook(raw)
	if err != nil {
		return "", err
	}
	var p WebhookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	header := func(name string) string {
		for k, v := range p.Headers {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	if c.Auth.Kind == "hmac" {
		body := []byte(p.Body)
		if p.BodyEncoding == "base64" {
			if body, err = base64.StdEncoding.DecodeString(p.Body); err != nil {
				return "", err
			}
		}
		if err := checkHMAC(c, header(c.Auth.Header), body, env); err != nil {
			return "", err
		}
	}
	switch {
	case c.Dedupe.Header != "":
		return header(c.Dedupe.Header), nil
	case c.Dedupe.Field != "" && p.JSON != nil:
		if v, ok := Lookup(p.JSON, c.Dedupe.Field); ok {
			return stringify(v), nil
		}
	}
	return "", nil
}

// EchoQuery 是来源配的握手参数（云端代收要同步回它）。
func EchoQuery(raw json.RawMessage) string {
	c, _ := parseWebhook(raw)
	return c.Verify.EchoQuery
}
