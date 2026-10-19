// Package inbox 是收件箱：项目在 inboxes/<id>.json 里声明「收哪些东西、收到交给哪个本机函数、要不要发通知」，
// 这里管配置、来源类型、事件和事件的存放。收进来、交给函数、发通知的流程在 internal/server/inbox.go。方案见 docs/inbox.md。
//
// 扩展一种能力就是加一种来源类型（Register 一个 SourceType）：管道、事件、处理、通知、管理页都不动。
// 现在只有 webhook；邮箱（mailbox）等来源只定了接口，没有实现。
package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// Dir 是项目里放收件箱声明的目录，一个收件箱一个文件，文件名就是它的 id。
const Dir = "inboxes"

// DefaultKeepDays：处理完的事件默认留几天。
const DefaultKeepDays = 7

// MaxAttempts：自动处理最多试几次，之后标成失败，等人点「重新处理」。
const MaxAttempts = 3

// Def 是一个收件箱的声明（inboxes/<id>.json）。
type Def struct {
	ID       string      `json:"-"` // 文件名；插件的带插件 id（social/comments）
	Name     string      `json:"name,omitempty"`
	Sources  []SourceDef `json:"sources"`
	Handler  string      `json:"handler"` // 本机函数：文件.函数
	Notify   []NotifyDef `json:"notify,omitempty"`
	KeepDays int         `json:"keep_days,omitempty"`
}

// Keep 是处理完的事件留多久。
func (d *Def) Keep() time.Duration {
	n := d.KeepDays
	if n <= 0 {
		n = DefaultKeepDays
	}
	return time.Duration(n) * 24 * time.Hour
}

// Source 按 id 找来源。
func (d *Def) Source(id string) *SourceDef {
	for i := range d.Sources {
		if d.Sources[i].ID == id {
			return &d.Sources[i]
		}
	}
	return nil
}

// SourceDef 是一个来源：id、type 是共有的，其余字段整个留给来源类型自己解析（Raw）。
type SourceDef struct {
	ID   string
	Type string
	Raw  json.RawMessage
}

func (s *SourceDef) UnmarshalJSON(b []byte) error {
	var head struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	s.ID, s.Type, s.Raw = head.ID, head.Type, append(json.RawMessage(nil), b...)
	return nil
}

func (s SourceDef) MarshalJSON() ([]byte, error) { return s.Raw, nil }

// NotifyDef 是一条通知规则。
type NotifyDef struct {
	When string          `json:"when"`           // received：收到就发（不跑代码、不调 AI）
	To   string          `json:"to"`             // secret:<名字>：密钥里存的机器人 webhook 地址
	Text string          `json:"text"`           // 字段路径替换：{{payload.json.name}}
	Body json.RawMessage `json:"body,omitempty"` // 发出去的 JSON，字符串里的 {{text}} 换成上面那段；不写是 {"text": …}
}

// ---- 来源类型 ----

type Mode int

const (
	Push Mode = iota // 别人调我们的地址（webhook）
	Pull             // 我们定时去读（邮箱账号…）
)

// Runner 是来源能在哪里跑。凭证存在跑它的地方。
type Runner string

const (
	RunLocal  Runner = "local"  // 本机 Annulo
	RunServer Runner = "server" // 服务端部署
	RunCloud  Runner = "cloud"  // 我们的云端（本地版的代收，没做）
)

// Env 是来源跑的时候能用的东西。
type Env struct {
	Secret func(name string) (string, bool)
}

// SourceType 是一种来源。
type SourceType interface {
	Type() string
	Mode() Mode
	Runners() []Runner
	Validate(cfg json.RawMessage) error
}

// Response 是推来源要直接回给对方的（握手这类），不存事件。
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

// PushSource：收一个请求，返回一条事件；只要回话、不存事件（握手）时 Event 是 nil、Response 不是 nil。
type PushSource interface {
	SourceType
	Handle(r *http.Request, body []byte, cfg json.RawMessage, env Env) (*Event, *Response, error)
}

// PullSource：从游标往后读，返回读到的事件和新的游标。游标要在事件存好之后才保存。
type PullSource interface {
	SourceType
	Poll(ctx context.Context, cfg json.RawMessage, cursor []byte, env Env) (events []Event, next []byte, err error)
}

var types = map[string]SourceType{}

// Register 登记一种来源类型。
func Register(t SourceType) { types[t.Type()] = t }

// TypeOf 找来源类型。
func TypeOf(name string) SourceType { return types[name] }

// ErrUnauthorized：推来源验证没过（签名不对）。
var ErrUnauthorized = i18n.New("签名验证没过", "Signature check failed")

// ---- 事件 ----

type Status string

const (
	Pending Status = "pending"
	Done    Status = "done"
	Failed  Status = "failed"
)

type SourceRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Event 是收进来的一条。外层固定，Payload 按来源类型定（webhook 见 WebhookPayload，邮件见 MailPayload）。
type Event struct {
	ID         string          `json:"id"`
	Inbox      string          `json:"inbox"`
	Source     SourceRef       `json:"source"`
	ReceivedAt time.Time       `json:"received_at"`
	DedupeKey  string          `json:"dedupe_key"`
	Payload    json.RawMessage `json:"payload"`
	Status     Status          `json:"status"`
	Attempts   int             `json:"attempts"`
	Error      string          `json:"error,omitempty"`
	HandledAt  *time.Time      `json:"handled_at,omitempty"`
	// NotifyError：收到时的通知没发出去（每条规则一行）；发成功是空
	NotifyError string `json:"notify_error,omitempty"`
	Test        bool   `json:"test,omitempty"` // 管理页「发一条测试」造的
	Via         string `json:"via,omitempty"`  // cloud：经云端代收进来的（docs/inbox.md）
}

// MailPayload 是邮件来源的 payload：不管从 IMAP、Gmail 还是 Outlook 来都是这一份（邮箱来源还没做，先定格式）。
type MailPayload struct {
	MessageID   string       `json:"message_id"`
	InReplyTo   string       `json:"in_reply_to,omitempty"`
	Thread      string       `json:"thread,omitempty"`
	From        MailAddr     `json:"from"`
	To          []MailAddr   `json:"to"`
	Cc          []MailAddr   `json:"cc,omitempty"`
	Subject     string       `json:"subject"`
	Text        string       `json:"text"`
	HTML        string       `json:"html,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Date        time.Time    `json:"date"`
	Folder      string       `json:"folder,omitempty"`
}

type MailAddr struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type Attachment struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size int64  `json:"size"`
	Ref  string `json:"ref"` // 存好的文件，handler 要用再读
}

// ---- 解析声明 ----

var (
	handlerRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.[A-Za-z_$][A-Za-z0-9_$]*$`)
	sourceRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// Parse 解析一个收件箱声明；rel 是报错时给人看的路径。不认识的来源类型、写错的字段直接报错，不静默忽略。
func Parse(id, rel string, data []byte) (Def, error) {
	var d Def
	if err := json.Unmarshal(data, &d); err != nil {
		return d, i18n.Errorf("%s 格式不对：%w", "%s is malformed: %w", rel, err)
	}
	d.ID = id
	if !handlerRe.MatchString(d.Handler) {
		return d, i18n.Errorf("%s：handler 要写成 文件.函数（比如 leads.receive）", "%s: handler must be file.function (e.g. leads.receive)", rel)
	}
	if len(d.Sources) == 0 {
		return d, i18n.Errorf("%s：sources 至少要有一个来源", "%s: sources needs at least one source", rel)
	}
	var seen []string
	for _, s := range d.Sources {
		if !sourceRe.MatchString(s.ID) {
			return d, i18n.Errorf("%s：来源的 id 只能用字母、数字、- 和 _（%q）", "%s: a source id may only use letters, digits, - and _ (%q)", rel, s.ID)
		}
		if slices.Contains(seen, s.ID) {
			return d, i18n.Errorf("%s：来源 id %q 重复了", "%s: source id %q is used twice", rel, s.ID)
		}
		seen = append(seen, s.ID)
		t := TypeOf(s.Type)
		if t == nil {
			return d, i18n.Errorf("%s：这版 Annulo 不认识来源类型 %q（现在支持：%s）", "%s: this Annulo doesn't know source type %q (supported: %s)", rel, s.Type, strings.Join(TypeNames(), ", "))
		}
		if err := t.Validate(s.Raw); err != nil {
			return d, i18n.Errorf("%s：来源 %s：%w", "%s: source %s: %w", rel, s.ID, err)
		}
	}
	for i, n := range d.Notify {
		if n.When != "received" {
			return d, i18n.Errorf("%s：notify 第 %d 条的 when 现在只能是 received", "%s: notify #%d: when can only be received for now", rel, i+1)
		}
		if !strings.HasPrefix(n.To, "secret:") || len(n.To) == len("secret:") {
			return d, i18n.Errorf("%s：notify 第 %d 条的 to 要写 secret:<密钥名>（密钥里存机器人的 webhook 地址）", "%s: notify #%d: to must be secret:<name> (a secret holding the bot's webhook URL)", rel, i+1)
		}
		if strings.TrimSpace(n.Text) == "" {
			return d, i18n.Errorf("%s：notify 第 %d 条缺 text", "%s: notify #%d is missing text", rel, i+1)
		}
		if len(n.Body) > 0 && !json.Valid(n.Body) {
			return d, i18n.Errorf("%s：notify 第 %d 条的 body 不是 JSON", "%s: notify #%d: body isn't JSON", rel, i+1)
		}
	}
	return d, nil
}

// TypeNames 是登记了的来源类型。
func TypeNames() []string {
	var out []string
	for k := range types {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
