package creght

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// Annulo 的云服务：订阅权益和收件箱代收（docs/billing.md、docs/inbox.md；平台那边见 weave docs/architecture/inbox-relay.md）。
// 「Annulo 订阅」就是 creght 账号的会员。内测期间只对 maintainer 开放，别的账号返回 ErrPreview：当作没有这项服务，界面不显示。

// ErrPreview：这项云服务还在内测，这个账号用不了。
var ErrPreview = i18n.New("这项云服务还在内测", "This cloud service is in private preview")

// QuotaError 是平台统一的额度拒绝：{ code: 4021, quota, reason, message, subscribe_url }。
type QuotaError struct {
	Status       int
	Quota        string // ai_credits / tunnel / inbox_relay / backup / assets / templates
	Reason       string // 细分原因，不认识就只看 Quota
	Message      string // 直接给用户看
	SubscribeURL string
}

func (e *QuotaError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return i18n.T("需要开通 Annulo 订阅", "This needs an Annulo subscription")
}

func (e *QuotaError) Is(target error) bool { return target == ErrSubscriptionRequired }

// Entitlements 是账号的 Annulo 订阅和各项额度（GET /api/u/annulo/entitlements）。界面原样显示，不在客户端判断。
type Entitlements struct {
	Plan struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Free bool   `json:"free"`
	} `json:"plan"`
	Status       string                     `json:"status"` // active / grace / expired / closed / free
	ExpiresAt    *time.Time                 `json:"expires_at"`
	GraceUntil   *time.Time                 `json:"grace_until"`
	Quotas       map[string]json.RawMessage `json:"quotas"`
	Templates    json.RawMessage            `json:"templates"`
	SubscribeURL string                     `json:"subscribe_url"`
}

func (c *Client) Entitlements(ctx context.Context) (*Entitlements, error) {
	var out Entitlements
	if err := preview(c.Get(ctx, "/api/u/annulo/entitlements", nil, &out)); err != nil {
		return nil, err
	}
	return &out, nil
}

// preview：这几个接口内测期间对别的账号回 403，换成 ErrPreview；平台还没有这个接口（404）也一样当没有这项服务。
func preview(err error) error {
	var se *StatusError
	if errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound) {
		return ErrPreview
	}
	return err
}

// ---- 收件箱代收 ----

// RelayNotify 是收到时的通知（云端发），规则和 internal/inbox 的一样，to 是机器人的地址本身。
type RelayNotify struct {
	To   string          `json:"to"`
	Text string          `json:"text"`
	Body json.RawMessage `json:"body,omitempty"`
}

type RelaySourceConfig struct {
	Verify *struct {
		EchoQuery string `json:"echo_query,omitempty"`
	} `json:"verify,omitempty"`
	Notify []RelayNotify `json:"notify,omitempty"`
}

// RelaySourceIn 注册 / 更新一个来源：按（账号、项目、收件箱、来源）认，配置整份替换。
type RelaySourceIn struct {
	Project string `json:"project"`
	Inbox   string `json:"inbox"`
	Source  string `json:"source"`
	Name    string `json:"name,omitempty"`
	RelaySourceConfig
}

type RelaySource struct {
	ID             string            `json:"id"`
	Project        string            `json:"project"`
	Inbox          string            `json:"inbox"`
	Source         string            `json:"source"`
	Name           string            `json:"name"`
	Config         RelaySourceConfig `json:"config"`
	LastReceivedAt *time.Time        `json:"last_received_at"`
	URL            string            `json:"url"`
}

func (c *Client) PutRelaySource(ctx context.Context, in RelaySourceIn) (*RelaySource, error) {
	var out RelaySource
	if err := preview(c.do(ctx, http.MethodPut, "/api/u/inbox_relay/source", nil, in, &out)); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RelaySources(ctx context.Context, project string) ([]RelaySource, error) {
	var out struct {
		List []RelaySource `json:"list"`
	}
	err := c.Get(ctx, "/api/u/inbox_relay/sources", url.Values{"project": {project}}, &out)
	return out.List, preview(err)
}

// DeleteRelaySource 删一个来源；source 为空删整个收件箱的。连云端的事件一起删，地址之后回 404。
func (c *Client) DeleteRelaySource(ctx context.Context, project, inbox, source string) error {
	q := url.Values{"project": {project}, "inbox": {inbox}}
	if source != "" {
		q.Set("source", source)
	}
	return c.do(ctx, http.MethodDelete, "/api/u/inbox_relay/source", q, nil, nil)
}

// RelayEvent 是云端收下的一条，payload 就是 inbox.WebhookPayload 的形状。
type RelayEvent struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Inbox   string `json:"inbox"`
	Source  struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"source"`
	ReceivedAt  time.Time       `json:"received_at"`
	DedupeKey   string          `json:"dedupe_key"`
	Payload     json.RawMessage `json:"payload"`
	NotifyError string          `json:"notify_error"`
}

// PullRelayEvents 拉一个项目待处理的（拉到的被领 5 分钟），more 是还有没有。
func (c *Client) PullRelayEvents(ctx context.Context, project string, limit int) ([]RelayEvent, bool, error) {
	var out struct {
		List []RelayEvent `json:"list"`
		More bool         `json:"more"`
	}
	err := c.Get(ctx, "/api/u/inbox_relay/events", url.Values{"project": {project}, "limit": {strconv.Itoa(limit)}}, &out)
	return out.List, out.More, preview(err)
}

type RelayAck struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AckRelayEvents 回报：回报过的（成功失败都算）云端不再给。
func (c *Client) AckRelayEvents(ctx context.Context, results []RelayAck) error {
	return c.do(ctx, http.MethodPost, "/api/u/inbox_relay/events/ack", nil, map[string]any{"results": results}, nil)
}
