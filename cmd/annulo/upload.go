package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	neturl "net/url"
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
//
// --local：复制进这台电脑上项目的本机文件（和页面的 POST local/files 同一条路），打印 local:<name>，不传云端。
// 视频这类大文件（最多 8GB）用它；只复制文件，Annulo 不记原来的路径。
func uploadCmd() *cobra.Command {
	var asJSON, local bool
	cmd := &cobra.Command{
		Use:   "upload <文件…>",
		Short: "把本机文件传进当前项目，打印地址（在线项目是 creght 素材，离线项目存在本机），比如 annulo upload cover.png",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			base := fmt.Sprintf("http://%s/_shuttle/api/", cfg.LocalAddr())
			var out []*uploaded
			for _, f := range args {
				var r *uploaded
				if local {
					r, err = uploadLocalFile(base+"local/files", f)
				} else {
					r, err = uploadFile(base+"local/upload", f)
				}
				if err != nil {
					return i18n.Errorf("上传 %s 失败：%w", "Failed to upload %s: %w", f, err)
				}
				r.File = f
				if !asJSON {
					if local {
						fmt.Println(r.Ref)
					} else {
						fmt.Println(r.URL)
					}
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON：[{file, url, size, content_type, existed}]；加 --local 时是 [{file, ref, url, size, content_type}]")
	cmd.Flags().BoolVar(&local, "local", false, "存在这台电脑上、不传云端（视频这类大文件，最多 8GB），打印 local:<name>：存进表、交给本机函数用；url 给页面预览")
	return cmd
}

type uploaded struct {
	File        string `json:"file"`
	Ref         string `json:"ref,omitempty"` // --local 才有：local:<name>
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
	req.Header.Set("content-type", mw.FormDataContentType())
	return postUpload(req)
}

// uploadLocalFile 把文件原样流给 POST local/files（请求体就是文件，头 X-Filename），存在本机。
func uploadLocalFile(url, path string) (*uploaded, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	} else if st.IsDir() {
		return nil, i18n.New("这是个目录，要给文件", "That's a directory; give a file")
	}
	req, _ := http.NewRequest("POST", url, f)
	req.ContentLength = st.Size()
	name := filepath.Base(path)
	req.Header.Set("X-Filename", neturl.QueryEscape(name))
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ct != "" {
		req.Header.Set("content-type", ct)
	}
	return postUpload(req)
}

func postUpload(req *http.Request) (*uploaded, error) {
	url := req.URL.String()
	req.Header.Set("X-Shuttle", "1")
	setProjectHeader(req)
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
