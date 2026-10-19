package server

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
)

// 页面上传文件拿公开地址：POST local/upload（multipart，字段 file），传到运营后台所在站点的 creght 素材里，
// 返回 { url, path, size, content_type, existed }。素材库、社媒配图、文章插图这些要给外部平台用的文件走它。
// 对话里附的图片走 agent/uploads（只存本机，给模型看），和这个不是一回事；助手自己传文件用 annulo upload（转发到这里）。
//
// 直接调 creght 的素材上传接口（和 creght upload 一样，用 CLI 的登录），文件先读进内存算哈希，所以设个上限。

const maxAssetUpload = 200 << 20

func (s *Server) apiAssetUpload(w http.ResponseWriter, r *http.Request) {
	if s.ws.ProjectID == "" || (s.ws.SiteID == "" && !s.ws.Offline) {
		fail(w, http.StatusConflict, i18n.New("还没打开运营后台，不知道传到哪个站点", "No back office is open, so there's no site to upload to"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAssetUpload+1<<20) // 表单本身的开销
	f, h, err := r.FormFile("file")
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			fail(w, http.StatusRequestEntityTooLarge, i18n.Errorf("文件太大：单个最多 %d MB", "File too large: at most %d MB each", maxAssetUpload>>20))
			return
		}
		fail(w, http.StatusBadRequest, i18n.New("要用 multipart 上传，字段名 file", "Upload as multipart with the field name file"))
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAssetUpload+1))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(b) > maxAssetUpload {
		fail(w, http.StatusRequestEntityTooLarge, i18n.Errorf("文件太大：单个最多 %d MB", "File too large: at most %d MB each", maxAssetUpload>>20))
		return
	}
	if len(b) == 0 {
		fail(w, http.StatusBadRequest, i18n.New("文件是空的", "The file is empty"))
		return
	}
	name := filepath.Base(h.Filename)
	ctype := h.Header.Get("Content-Type")
	if ctype == "" || ctype == "application/octet-stream" {
		ctype = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}
	if ctype == "" {
		ctype = http.DetectContentType(b)
	}
	a, err := s.storeAsset(r.Context(), s.ws, name, ctype, b)
	if err != nil {
		code := http.StatusBadGateway
		switch {
		case s.ws.Offline:
			code = http.StatusInternalServerError
		case errors.Is(err, creght.ErrNotLoggedIn):
			code = http.StatusUnauthorized
		}
		fail(w, code, err)
		return
	}
	writeJSON(w, a)
}

// storeAsset 把一个文件存成运营后台的素材，拿长期能用的地址：离线项目存本机，在线项目传到站点的 creght 素材。
// 页面上传（local/upload）和本机函数的 ctx.upload 共用。
func (s *Server) storeAsset(ctx context.Context, ws *creght.Workspace, name, ctype string, b []byte) (*creght.Asset, error) {
	if ws.Offline {
		return s.saveOfflineAsset(name, ctype, b)
	}
	if ws.ProjectID == "" || ws.SiteID == "" {
		return nil, i18n.New("还没打开运营后台，不知道传到哪个站点", "No back office is open, so there's no site to upload to")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	a, err := creght.NewClient(ws.APIHost).UploadAsset(ctx, ws.ProjectID, ws.SiteID, name, ctype, b)
	if err != nil {
		return nil, i18n.Errorf("上传到 creght 失败：%w", "Upload to creght failed: %w", err)
	}
	return a, nil
}

// uploadHost 是 ctx.upload 存文件的那一步（下载在 localfn 里做）：存进 ws 这个项目的素材
func (s *Server) uploadHost(ws *creght.Workspace) func(ctx context.Context, name, ctype string, b []byte) (map[string]any, error) {
	return func(ctx context.Context, name, ctype string, b []byte) (map[string]any, error) {
		a, err := s.storeAsset(ctx, ws, name, ctype, b)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": a.URL, "size": a.Size, "content_type": a.ContentType, "existed": a.Existed}, nil
	}
}
