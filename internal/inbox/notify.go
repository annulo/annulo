package inbox

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

var notifyClient = &http.Client{Timeout: 15 * time.Second}

// SendNotify 发一条通知：POST 到 to 指向的密钥里存的地址（飞书、企业微信、钉钉、Slack 的机器人都是一个地址）。
// 不认识具体平台：各家要的 JSON 格式写在 body 里。
func SendNotify(ctx context.Context, n NotifyDef, e Event, env Env) error {
	name := strings.TrimPrefix(n.To, "secret:")
	target, ok := "", false
	if env.Secret != nil {
		target, ok = env.Secret(name)
	}
	target = strings.TrimSpace(target)
	if !ok || target == "" {
		return i18n.Errorf("没有密钥 %s：到 设置 → 密钥 里加上机器人的 webhook 地址", "Secret %s is missing: add the bot's webhook URL in Settings → Secrets", name)
	}
	if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		return i18n.Errorf("密钥 %s 不是一个网址", "Secret %s isn't a URL", name)
	}
	body, err := RenderNotify(n, e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := notifyClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return i18n.Errorf("通知发送失败：HTTP %d %s", "Notification failed: HTTP %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
