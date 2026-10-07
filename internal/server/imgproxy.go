package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// 图片代理：GET /_shuttle/img?url=<图片地址>
//
// 很多平台的图床有防盗链（小红书、公众号…）：浏览器在 localhost 页面里直接 <img src> 会带上
// Referer 和浏览器的请求特征，被拒成 403。页面把地址交给本机，由 Shuttle 不带 Referer 去取，原样返回。
// 和具体平台无关，只是「在本机取一张公开的图片」。
//
//   - 只取 http / https，不能访问本机和内网（和本机函数的 fetch 同一套规则）；
//   - 只返回图片（image/*），单张最多 20MB；
//   - 只给 Shuttle 自己的页面用：浏览器标明是别的网站发起的（Sec-Fetch-Site: cross-site）就拒绝，免得成了开放代理；
//   - 缓存在 ~/.shuttle/cache/img，浏览器也缓存 7 天。

const (
	imgMaxBytes = 20 << 20
	imgCacheTTL = 7 * 24 * time.Hour
)

type imgMeta struct {
	ContentType string    `json:"content_type"`
	URL         string    `json:"url"`
	FetchedAt   time.Time `json:"fetched_at"`
}

func (s *Server) imgCacheDir() string { return filepath.Join(s.cfg.Dir, "cache", "img") }

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, i18n.T("只支持 GET", "Only GET is supported"), http.StatusMethodNotAllowed)
		return
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		http.Error(w, i18n.T("图片代理只给 Annulo 自己的页面用", "The image proxy is only for Annulo's own pages"), http.StatusForbidden)
		return
	}
	raw := r.URL.Query().Get("url")
	// 离线项目上传的文件存在本机（/_annulo/uploaded/…，老数据是带端口的完整地址）：出网规则不让访问本机，直接读文件
	if p := s.localAssetFile(raw); p != "" {
		w.Header().Set("x-content-type-options", "nosniff")
		w.Header().Set("content-security-policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		http.ServeFile(w, r, p)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, i18n.T("url 要是 http / https 的图片地址", "url must be an http / https image URL"), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(u.String()))
	key := hex.EncodeToString(sum[:])
	body, meta := s.imgCached(key)
	if body == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		body, meta, err = fetchImage(ctx, u)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.imgStore(key, body, meta)
	}
	w.Header().Set("content-type", meta.ContentType)
	w.Header().Set("cache-control", fmt.Sprintf("private, max-age=%d", int(imgCacheTTL.Seconds())))
	w.Header().Set("x-content-type-options", "nosniff")
	// 不要当成页面打开（SVG 里能带脚本）
	w.Header().Set("content-security-policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Write(body)
}

func (s *Server) imgCached(key string) ([]byte, imgMeta) {
	dir := s.imgCacheDir()
	var m imgMeta
	b, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil || json.Unmarshal(b, &m) != nil || time.Since(m.FetchedAt) > imgCacheTTL {
		return nil, m
	}
	body, err := os.ReadFile(filepath.Join(dir, key))
	if err != nil {
		return nil, m
	}
	return body, m
}

func (s *Server) imgStore(key string, body []byte, m imgMeta) {
	dir := s.imgCacheDir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	b, _ := json.Marshal(m)
	// 先写内容再写元信息：元信息在，内容就一定完整
	if os.WriteFile(filepath.Join(dir, key), body, 0o600) == nil {
		os.WriteFile(filepath.Join(dir, key+".json"), b, 0o600)
	}
}

// fetchImage 不带 Referer、不带 Cookie 去取一张图片。
func fetchImage(ctx context.Context, u *url.URL) ([]byte, imgMeta, error) {
	if err := checkTarget(u.Hostname()); err != nil {
		return nil, imgMeta{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, imgMeta{}, err
	}
	req.Header.Set("user-agent", "Shuttle-image-proxy")
	req.Header.Set("accept", "image/avif,image/webp,image/png,image/jpeg,image/*;q=0.8")
	resp, err := fetchClient().Do(req)
	if err != nil {
		return nil, imgMeta{}, i18n.Errorf("取图片失败：%w", "Failed to fetch the image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imgMeta{}, i18n.Errorf("图片地址返回 %d", "The image URL returned %d", resp.StatusCode)
	}
	ct := resp.Header.Get("content-type")
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		return nil, imgMeta{}, i18n.Errorf("这个地址不是图片（%s）", "This URL isn't an image (%s)", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, imgMaxBytes+1))
	if err != nil {
		return nil, imgMeta{}, i18n.Errorf("读图片失败：%w", "Failed to read the image: %w", err)
	}
	if len(body) > imgMaxBytes {
		return nil, imgMeta{}, i18n.New("图片超过 20MB", "The image is larger than 20MB")
	}
	return body, imgMeta{ContentType: ct, URL: u.String(), FetchedAt: time.Now()}, nil
}
