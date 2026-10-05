package creght

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/br41n10/qetag"
)

// 站点素材上传，和 creght upload 走同一套：预上传（按内容哈希查重）→ PUT 到签名地址 → 确认。
// 内容已经传过的（hash_exist）平台只建一条记录，跳过后两步。

type Asset struct {
	URL         string `json:"url"`
	Path        string `json:"path"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
	Existed     bool   `json:"existed"` // 同样的内容以前传过，这次没再传文件
}

func (c *Client) UploadAsset(ctx context.Context, projectID, siteID, name, contentType string, body []byte) (*Asset, error) {
	qe := qetag.New()
	if _, err := qe.Write(body); err != nil {
		return nil, i18n.Errorf("算文件哈希：%w", "Hashing the file: %w", err)
	}
	var pre struct {
		HashExist    bool   `json:"hash_exist"`
		PresignedURL string `json:"presigned_url"`
		FilePath     string `json:"file_path"`
		FileURL      string `json:"file_url"`
		ID           int64  `json:"id"`
	}
	path := fmt.Sprintf("/api/u/project/%s/site/%s/file/s3_pre_upload", url.PathEscape(projectID), url.PathEscape(siteID))
	req := map[string]any{"file_name": name, "hash": qe.Etag(), "mimetype": contentType, "size": len(body), "from": "user"}
	if err := c.do(ctx, http.MethodPost, path, nil, req, &pre); err != nil {
		return nil, err
	}
	if !pre.HashExist {
		if strings.TrimSpace(pre.PresignedURL) == "" {
			return nil, i18n.New("creght 没给上传地址", "creght returned no upload URL")
		}
		if err := putObject(ctx, pre.PresignedURL, contentType, body); err != nil {
			return nil, err
		}
		if err := c.do(ctx, http.MethodPost, "/api/u/file/ack_s3_upload", nil, map[string]int64{"id": pre.ID}, nil); err != nil {
			return nil, err
		}
	}
	return &Asset{URL: pre.FileURL, Path: pre.FilePath, Size: len(body), ContentType: contentType, Existed: pre.HashExist}, nil
}

// putObject 直接传到对象存储的签名地址（不带 creght 的 token）。大文件慢，给足时间。
func putObject(ctx context.Context, rawURL, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return i18n.Errorf("上传文件：%w", "Uploading the file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return i18n.Errorf("上传文件：HTTP %d", "Uploading the file: HTTP %d", resp.StatusCode)
	}
	return nil
}
