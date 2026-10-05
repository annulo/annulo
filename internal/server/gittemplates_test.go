package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/wsgit"
)

// 本机的 git 模板仓库：模板在 blank/ 子目录，打了 v1.0.0。
func localGitTemplate(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "blank"), 0o755)
	os.WriteFile(filepath.Join(repo, "blank", "annulo.json"), []byte(`{"name": "空白模板"}`), 0o644)
	os.MkdirAll(filepath.Join(repo, "blank", "pages"), 0o755)
	os.WriteFile(filepath.Join(repo, "blank", "pages", "index.tsx"), []byte("export default () => null\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "v1"}, {"tag", "-a", "v1.0.0", "-m", "第一版"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%s", args, out)
		}
	}
	return repo
}

func TestGitTemplatesListed(t *testing.T) {
	host := "https://not-signed-in.invalid" // 这台机器没登录过它：git 模板排前面
	stubTemplates(t, map[string][]creght.OpsTemplate{host: {{ProjectID: "p1", SiteID: "s1", NameLocales: map[string]string{"zh-CN": "外贸助手"}}}})
	repo := localGitTemplate(t)
	old := defaultTemplateSources
	defaultTemplateSources = nil // 测试不连 GitHub
	t.Cleanup(func() { defaultTemplateSources = old })
	setTemplateSources(t.TempDir(), []string{repo + "#blank", repo + "#blank"}) // 重复的只列一次
	t.Cleanup(func() { setTemplateSources("", nil) })
	gitMetaCache.Lock()
	gitMetaCache.m = nil
	gitMetaCache.Unlock()

	list := templatesOn(host)
	if len(list) != 1 || list[0].Git != wsgit.GitSite(repo, "blank") || list[0].Name() != "空白模板" {
		t.Fatalf("没连 creght：只有 git 模板，读到名字：%+v", list)
	}
	creght.StoreToken(host, "tok", time.Now().Add(time.Hour))
	if list := templatesOn(host); len(list) != 2 || list[0].ProjectID != "p1" || list[1].Git == "" {
		t.Fatalf("连着 creght：creght 的排前面：%+v", list)
	}
	if list[0].PreviewURL() != "" || list[0].Site() != list[0].Git || list[0].Key != list[0].Git {
		t.Fatalf("git 模板没有预览地址，站点和 key 都是 git:…：%+v", list[0])
	}
	if creghtTemplatesOn(host)[0].Git != "" {
		t.Fatal("在线项目只从 creght 模板复制")
	}
	// 设置里删掉了这个 git 模板，从它建的项目照样认得
	setTemplateSources(t.TempDir(), nil)
	if tpl := templateBySite(wsgit.GitSite(repo, "blank")); tpl == nil || tpl.Git == "" {
		t.Fatal("删掉的 git 模板也要认得")
	}
}

func TestTemplateSourceAddRemove(t *testing.T) {
	stubTemplates(t, nil)
	repo := localGitTemplate(t)
	dir := t.TempDir()
	s := &Server{cfg: &config.Config{Dir: dir}}
	setTemplateSources(dir, nil)
	t.Cleanup(func() { setTemplateSources("", nil) })
	call := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/_shuttle/api/"+path, strings.NewReader(body))
		if method == http.MethodPost {
			s.apiTemplateSourceAdd(w, r)
		} else {
			s.apiTemplateSourceRemove(w, r)
		}
		return w.Code, w.Body.String()
	}
	if code, body := call(http.MethodPost, "settings/templates", srcBody(repo+"#nope")); code != 400 || !strings.Contains(body, "子目录") {
		t.Fatalf("子目录不对：%d %s", code, body)
	}
	if code, body := call(http.MethodPost, "settings/templates", srcBody(filepath.Join(t.TempDir(), "x"))); code != 400 || !strings.Contains(body, "模板仓库") {
		t.Fatalf("仓库不存在：%d %s", code, body)
	}
	code, body := call(http.MethodPost, "settings/templates", srcBody(" git:"+repo+" # blank/ "))
	if code != 200 || !strings.Contains(body, "空白模板") || !strings.Contains(body, `"removable":true`) {
		t.Fatalf("加上：%d %s", code, body)
	}
	if len(s.cfg.TemplateSources) != 1 || s.cfg.TemplateSources[0] != repo+"#blank" || len(gitTemplates()) != 1 {
		t.Fatalf("存进配置：%v", s.cfg.TemplateSources)
	}
	call(http.MethodPost, "settings/templates", srcBody(repo+"#blank")) // 重复加不会多一条
	if len(s.cfg.TemplateSources) != 1 {
		t.Fatal(s.cfg.TemplateSources)
	}
	if code, _ := call(http.MethodDelete, "settings/templates?key="+url.QueryEscape(wsgit.GitSite(repo, "blank")), ""); code != 200 || len(gitTemplates()) != 0 {
		t.Fatal("移除")
	}
}

func srcBody(source string) string {
	b, _ := json.Marshal(map[string]string{"source": source})
	return string(b)
}
