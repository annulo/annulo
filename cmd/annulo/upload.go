package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
)

// annulo upload <文件…>：把本机文件传进当前项目，拿到能存进表、写进页面的地址。和页面上传（POST local/upload）是同一条路：
// 在线项目传到项目站点的 creght 素材（https 地址），离线项目存在本机（/_annulo/uploaded/…，不带端口）。
// 助手自己生成、下载的图要进素材库、配进文章时用它，不用管端口和站点 id。
func uploadCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "upload <文件…>",
		Short: "把本机文件传进当前项目，打印地址（在线项目是 creght 素材，离线项目存在本机），比如 annulo upload cover.png",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			base := fmt.Sprintf("http://%s/_shuttle/api/", cfg.Addr())
			var out []*uploaded
			for _, f := range args {
				r, err := uploadFile(base+"local/upload", f)
				if err != nil {
					return i18n.Errorf("上传 %s 失败：%w", "Failed to upload %s: %w", f, err)
				}
				r.File = f
				if !asJSON {
					fmt.Println(r.URL)
				}
				out = append(out, r)
			}
			if asJSON {
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON：[{file, url, size, content_type, existed}]")
	return cmd
}

type uploaded struct {
	File        string `json:"file"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Existed     bool   `json:"existed"`
}

func uploadFile(url, path string) (*uploaded, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if st.IsDir() {
		return nil, i18n.New("这是个目录，要给文件", "That's a directory; give a file")
	}
	// 边读边传（单个最多 200MB，不整个读进内存）
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		h := textproto.MIMEHeader{}
		name := filepath.Base(path)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
		if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ct != "" {
			h.Set("Content-Type", ct)
		}
		part, err := mw.CreatePart(h)
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, _ := http.NewRequest("POST", url, pr)
	req.Header.Set("X-Shuttle", "1")
	req.Header.Set("content-type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, i18n.Errorf("连不上本机的 Annulo（%s）：先打开 Annulo App", "Can't reach Annulo on this machine (%s): open the Annulo app first", url)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, fmt.Errorf("%s", e.Error)
	}
	var r uploaded
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
