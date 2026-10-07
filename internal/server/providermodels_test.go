package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

// 拉模型列表：OpenAI 兼容走 /models + Bearer，Anthropic 走 /v1/models + x-api-key；key 不对给一句能看懂的
func TestProviderModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models" && r.Header.Get("authorization") == "Bearer k":
			w.Write([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"a"}]}`))
		case r.URL.Path == "/v1/models" && r.Header.Get("x-api-key") == "k" && r.Header.Get("anthropic-version") != "":
			w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
		case r.URL.Path == "/nolist/models":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	ids, err := providerModels(context.Background(), config.Provider{Api: "openai-completions", BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil || !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("openai: %v %v", ids, err)
	}
	ids, err = providerModels(context.Background(), config.Provider{Api: "anthropic-messages", BaseURL: srv.URL, APIKey: "k"})
	if err != nil || !slices.Equal(ids, []string{"claude-x"}) {
		t.Fatalf("anthropic: %v %v", ids, err)
	}
	// 401 分不清是 key 不对还是网关不开放列模型：都算没拉到列表（页面手填，保存前测一次对话）
	var un unsupportedErr
	if _, err = providerModels(context.Background(), config.Provider{Api: "openai-completions", BaseURL: srv.URL + "/v1", APIKey: "bad"}); !errors.As(err, &un) {
		t.Fatalf("401 算没拉到列表：%v", err)
	}
	if _, err = providerModels(context.Background(), config.Provider{Api: "openai-completions", BaseURL: "http://127.0.0.1:1", APIKey: "k"}); err == nil || errors.As(err, &un) {
		t.Fatalf("连不上应该直接报错：%v", err)
	}
	if _, err = providerModels(context.Background(), config.Provider{Api: "openai-completions", BaseURL: srv.URL + "/nolist", APIKey: "k"}); !errors.As(err, &un) {
		t.Fatalf("404 算不提供列表：%v", err)
	}
}
