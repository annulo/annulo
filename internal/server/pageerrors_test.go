package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/creght"
)

func TestPageErrors(t *testing.T) {
	s := &Server{ws: &creght.Workspace{ProjectID: "p1"}}
	call := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiPage(rec, httptest.NewRequest(method, "/_shuttle/api/ui/page", strings.NewReader(body)))
		return rec
	}

	// 外壳没来过：窗口没开
	if out := s.checkPage(context.Background(), false, "", 0); out["open"] != false {
		t.Fatalf("没开窗口应该 open=false：%v", out)
	}

	call(http.MethodPost, `{"load_id":"a","path":"/","errors":[{"message":"TypeError: x","source":"render"}]}`)
	out := s.checkPage(context.Background(), false, "", 0)
	if errs, _ := out["errors"].([]pageError); out["open"] != true || len(errs) != 1 || errs[0].Message != "TypeError: x" {
		t.Fatalf("当前页面的报错：%v", out)
	}

	// 要刷新：外壳领到指令、重新加载（新的 load_id）后才返回，看到的是新加载的报错
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(20 * time.Millisecond)
			if strings.Contains(call(http.MethodGet, "").Body.String(), `"cmd_seq":1`) {
				call(http.MethodPost, `{"load_id":"b","path":"/geo","errors":[]}`)
				return
			}
		}
	}()
	out = s.checkPage(context.Background(), true, "geo", 0)
	if errs, _ := out["errors"].([]pageError); out["page"] != "/geo" || len(errs) != 0 {
		t.Fatalf("刷新后：%v", out)
	}
	if !strings.Contains(call(http.MethodGet, "").Body.String(), `"cmd_path":"/geo"`) {
		t.Error("切页指令要带上路径")
	}

	// 换了项目：清掉上一个项目的页面状态
	s.ws = &creght.Workspace{ProjectID: "p2"}
	call(http.MethodGet, "")
	if out := s.checkPage(context.Background(), false, "", 0); out["page"] != "" {
		t.Errorf("换项目后不该还是旧页面：%v", out)
	}
}
