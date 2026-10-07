package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/sky-valley/pi/ai"
)

// 对话里的图片：先传到本机 ~/.shuttle/uploads，消息里只存引用（/_shuttle/uploads/<name>），
// 对话历史 JSONL 不塞 base64。发给模型时再读文件；同时把本机路径告诉 agent，它能用
// annulo upload 把图放进文章、资料库。文件名是内容的 sha256，同一张图只存一份。

const MaxUploadBytes = 20 << 20

// UploadURLPrefix 是界面引用图片的地址前缀，服务端按它把消息里的图片映射回文件。
const UploadURLPrefix = "/_shuttle/uploads/"

var uploadExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

var uploadNameRe = regexp.MustCompile(`^[0-9a-f]{64}\.(png|jpg|gif|webp)$`)

func (a *Agent) uploadsDir() string { return filepath.Join(a.cfg.Dir, "uploads") }

// SaveUpload 存一张图，返回文件名。只收 png / jpeg / gif / webp，按内容判断类型，不信请求头。
func (a *Agent) SaveUpload(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxUploadBytes {
		return "", i18n.Errorf("图片超过 %d MB", "The image is larger than %d MB", MaxUploadBytes>>20)
	}
	if len(b) == 0 {
		return "", i18n.New("图片是空的", "The image is empty")
	}
	ext, ok := uploadExt[http.DetectContentType(b)]
	if !ok {
		return "", i18n.New("只支持 PNG、JPEG、GIF、WebP 图片", "Only PNG, JPEG, GIF and WebP images are supported")
	}
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:]) + ext
	dir := a.uploadsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err == nil {
		return name, nil
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return name, os.Rename(tmp, p)
}

// UploadPath 把文件名转成本机路径；名字不合法（比如想跳出目录）返回空。
func (a *Agent) UploadPath(name string) string {
	if !uploadNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(a.uploadsDir(), name)
}

func loadImage(path string) (ai.ImageContent, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ai.ImageContent{}, i18n.Errorf("读不到图片 %s：%w", "Can't read image %s: %w", filepath.Base(path), err)
	}
	return ai.ImageContent{Data: base64.StdEncoding.EncodeToString(b), MimeType: http.DetectContentType(b)}, nil
}
