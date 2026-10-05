package localfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/dop251/goja"
)

// 浏览器操作的「现场」：平台改了页面，点不到、等不到的时候，把当时的截图、页面上能操作的元素、页面源码存下来，
// 报错里带上目录（err.snapshot），自检和修复（助手照着现场改选择器）都靠它。存在 Host.SnapshotDir/<时间>-<profile>-<n>/：
//
//	shot.png      截图（当前视口）
//	outline.txt   页面上看得见的可操作元素：标签、role、aria-label、文字、placeholder、data-* 属性（改选择器主要看它）
//	page.html     页面源码（最多 2MB）
//	meta.json     { url, op, target, error, profile, at }
//
// 一次运行最多自动存 maxAutoSnapshots 份；目录里只留最近 keepSnapshots 份。

const (
	maxAutoSnapshots = 3
	keepSnapshots    = 50
	maxSnapshotHTML  = 2 << 20
)

// outlineJS 列出页面上看得见的可操作元素，一行一个（最多 400 行）。
const outlineJS = `(() => {
  const out = [];
  const seen = new Set();
  const sel = 'a[href],button,input,textarea,select,[role],[contenteditable="true"],[aria-label],[data-testid],[data-view-name],[data-control-name],h1,h2,h3,[aria-modal="true"]';
  for (const e of document.querySelectorAll(sel)) {
    if (out.length >= 400) break;
    if (seen.has(e)) continue;
    seen.add(e);
    const r = e.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) continue;
    const s = getComputedStyle(e);
    if (s.visibility === 'hidden' || s.display === 'none') continue;
    const parts = [e.tagName.toLowerCase()];
    if (e.id) parts.push('#' + e.id);
    for (const a of ['role', 'aria-label', 'name', 'type', 'placeholder', 'contenteditable', 'aria-modal', 'aria-disabled', 'disabled']) {
      const v = e.getAttribute(a);
      if (v != null && v !== '') parts.push(a + '="' + v.slice(0, 60) + '"');
    }
    for (const a of e.attributes) if (a.name.startsWith('data-') && a.value.length < 80) parts.push(a.name + '="' + a.value + '"');
    const t = (e.innerText || e.value || '').replace(/\s+/g, ' ').trim().slice(0, 60);
    if (t) parts.push('"' + t + '"');
    out.push(parts.join(' '));
  }
  return location.href + '\n' + document.title + '\n\n' + out.join('\n');
})()`

// snapshot 存一份现场，返回目录；存不了返回 ""（不影响原来的报错）。
func (r *runner) snapshot(p BrowserPage, profile, op, target string, cause error) string {
	if r.h.SnapshotDir == "" {
		return ""
	}
	// 运行已经被停止 / 超时时 r.ctx 不能用了，现场每一步单独给 15 秒：截图慢（机器忙、页面大）时不连累后面几份
	step := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	}
	r.nsnap++
	name := fmt.Sprintf("%s-%s-%d", time.Now().Format("20060102-150405"), safeName(profile), r.nsnap)
	dir := filepath.Join(r.h.SnapshotDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	// 最有用的 outline 先存
	ctx, cancel := step()
	url, _ := p.URL(ctx)
	if v, err := p.Eval(ctx, outlineJS); err == nil {
		if s, ok := v.(string); ok {
			os.WriteFile(filepath.Join(dir, "outline.txt"), []byte(s), 0o600)
		}
	}
	cancel()
	ctx, cancel = step()
	if h, err := p.HTML(ctx, ""); err == nil {
		if len(h) > maxSnapshotHTML {
			h = h[:maxSnapshotHTML]
		}
		os.WriteFile(filepath.Join(dir, "page.html"), []byte(h), 0o600)
	}
	cancel()
	ctx, cancel = step()
	if png, err := p.Screenshot(ctx, "", false); err == nil {
		os.WriteFile(filepath.Join(dir, "shot.png"), png, 0o600)
	}
	cancel()
	meta := map[string]any{"url": url, "op": op, "target": target, "profile": profile, "at": time.Now().Format(time.RFC3339)}
	if cause != nil {
		meta["error"] = cause.Error()
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)
	pruneSnapshots(r.h.SnapshotDir, keepSnapshots)
	return dir
}

// failed 是浏览器操作失败：自动存现场（一次运行最多 maxAutoSnapshots 份），报错里带上目录，err.snapshot 也能拿到。
func (r *runner) failed(p BrowserPage, profile, op, target string, err error) *goja.Promise {
	dir := ""
	if r.nsnap < maxAutoSnapshots && !errors.Is(err, context.Canceled) && r.ctx.Err() == nil {
		dir = r.snapshot(p, profile, op, target, err)
	}
	if dir != "" {
		err = fmt.Errorf("%w%s", err, i18n.Tf("（现场：%s）", " (snapshot: %s)", dir))
	}
	e := r.vm.NewGoError(err)
	if dir != "" {
		e.Set("snapshot", dir)
	}
	pr, _, reject := r.vm.NewPromise()
	reject(e)
	return pr
}

func safeName(s string) string {
	s = strings.Map(func(c rune) rune {
		if c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return c
		}
		return '_'
	}, s)
	if s == "" {
		return "page"
	}
	return s
}

// pruneSnapshots 只留最近 keep 份（目录名以时间开头，按名字排序就是按时间）。
func pruneSnapshots(root string, keep int) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return
	}
	sort.Strings(dirs)
	for _, d := range dirs[:len(dirs)-keep] {
		os.RemoveAll(filepath.Join(root, d))
	}
}
