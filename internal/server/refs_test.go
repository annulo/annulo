package server

import (
	"encoding/json"
	"testing"
)

func TestRefRe(t *testing.T) {
	got := refRe.FindAllStringSubmatch("看下 @articles:r65dd954c26dfdefa1 和 @assets:r65dda#3，别认 https://x.com 和 10:30", -1)
	if len(got) != 2 || got[0][1] != "articles" || got[1][2] != "r65dda" || got[1][3] != "3" {
		t.Fatalf("%q", got)
	}
}

// 标题：title_field → title/name/subject/label → schema 里第一个像标题的字符串字段
func TestRefTitle(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"url":{},"summary":{},"body":{}}}`)
	if v := refTitle(map[string]any{"title": "番茄", "name": "x"}, tableMeta{}); v != "番茄" {
		t.Fatal(v)
	}
	if v := refTitle(map[string]any{"subject": "主题", "title": "x"}, tableMeta{TitleField: "subject"}); v != "主题" {
		t.Fatal(v)
	}
	if v := refTitle(map[string]any{"url": "local:abc.png", "summary": "一句话\n概括", "body": "正文"}, tableMeta{JSONSchema: schema}); v != "一句话 概括" {
		t.Fatal(v)
	}
	if v := refTitle(map[string]any{"n": 3.0}, tableMeta{}); v != "" {
		t.Fatal(v)
	}
}

// 文件列表：files（对象数组）→ images（JSON 字符串数组）→ url；files_field 指定时只看它
func TestRefFiles(t *testing.T) {
	fs := refFiles(map[string]any{"files": []any{map[string]any{"url": "local:aa.png"}, map[string]any{"url": "local:bb.mp4", "mime": "video/mp4"}}}, "")
	if len(fs) != 2 || fs[0].Mime != "image/png" || fs[1].Mime != "video/mp4" {
		t.Fatalf("%+v", fs)
	}
	fs = refFiles(map[string]any{"images": `["https://x.com/a.jpg","https://x.com/b.webp"]`}, "")
	if len(fs) != 2 || fs[1].Mime != "image/webp" {
		t.Fatalf("%+v", fs)
	}
	fs = refFiles(map[string]any{"url": "local:cc.html", "images": `["x.png"]`}, "photos")
	if len(fs) != 1 || fs[0].URL != "local:cc.html" {
		t.Fatalf("files_field 没有时退回 url：%+v", fs)
	}
	if refThumb("local:cc.png") != "/_shuttle/files/cc.png" {
		t.Fatal(refThumb("local:cc.png"))
	}
}
