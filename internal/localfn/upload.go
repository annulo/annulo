package localfn

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/dop251/goja"
)

// ctx.upload 一次最多存这么大（和页面上传 local/upload 一样）
const maxUpload = 200 << 20

// upload：await ctx.upload({ url, name })。按 fetch 的出网规则把网址下载到临时目录，再存成运营后台的素材
// （离线项目存本机，在线项目传到站点的 creght 素材），返回 { url, size, content_type, existed }。
// 外部平台给的图片地址常常是临时的（Notion 的几分钟就失效、要登录才能看），放进文章、配图之前用它换成长期地址。
// name 是文件名（决定扩展名），不给就取网址最后一段。参数是对象：以后加别的来源（比如 { base64, name }）不改用法。
func (r *runner) upload(arg goja.Value) *goja.Promise {
	if r.h.Upload == nil {
		return r.rejected(i18n.New("这里不能用 ctx.upload", "ctx.upload isn't available here"))
	}
	var o map[string]any
	if arg != nil && !goja.IsUndefined(arg) && !goja.IsNull(arg) {
		o, _ = toPlain(arg.Export()).(map[string]any)
	}
	raw, _ := o["url"].(string)
	if o == nil || raw == "" {
		return r.rejected(i18n.New("ctx.upload 要传对象：ctx.upload({ url, name })", "ctx.upload takes an object: ctx.upload({ url, name })"))
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return r.rejected(i18n.Errorf("ctx.upload 只能传 http / https 地址：%q", "ctx.upload only takes http / https URLs: %q", raw))
	}
	name, _ := o["name"].(string)
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" {
		name = ""
	}

	var body []byte
	ctype := ""
	if r.h.Download != nil {
		_, tmp := r.tempFile("upload", ".bin")
		defer os.Remove(tmp)
		ct, err := r.h.Download(r.ctx, raw, tmp)
		if err != nil {
			return r.rejected(i18n.Errorf("下载 %s 失败：%w", "Failed to download %s: %w", raw, err))
		}
		if st, err := os.Stat(tmp); err == nil && st.Size() > maxUpload {
			return r.rejected(i18n.Errorf("文件太大：单个最多 %d MB", "File too large: at most %d MB each", maxUpload>>20))
		}
		if body, err = os.ReadFile(tmp); err != nil {
			return r.rejected(err)
		}
		ctype = ct
	} else {
		resp, err := r.h.Fetch(r.ctx, FetchRequest{URL: raw, Method: "GET", Headers: map[string]string{}})
		if err != nil {
			return r.rejected(i18n.Errorf("下载 %s 失败：%w", "Failed to download %s: %w", raw, err))
		}
		if resp.Status < 200 || resp.Status >= 300 || resp.Truncated {
			return r.rejected(i18n.Errorf("下载 %s 失败：%d", "Failed to download %s: %d", raw, resp.Status))
		}
		body, ctype = resp.Body, header(resp.Headers, "content-type")
	}
	if len(body) == 0 {
		return r.rejected(i18n.Errorf("下载 %s 得到的是空文件", "Downloading %s gave an empty file", raw))
	}
	if mt, _, _ := mime.ParseMediaType(ctype); mt == "" || mt == "application/octet-stream" || mt == "binary/octet-stream" {
		ctype = ""
	}
	if name == "" {
		if seg, err := url.PathUnescape(path.Base(u.Path)); err == nil && seg != "/" && seg != "." {
			name = seg
		}
	}
	if ctype == "" {
		ctype = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}
	if ctype == "" {
		ctype = http.DetectContentType(body)
	}
	if name == "" || filepath.Ext(name) == "" {
		name = strings.TrimSuffix(name, ".") + fileExt(ctype, raw)
		if strings.HasPrefix(name, ".") {
			name = "file" + name
		}
	}
	res, err := r.h.Upload(r.ctx, name, ctype, body)
	if err != nil {
		return r.rejected(err)
	}
	return r.resolved(res)
}
