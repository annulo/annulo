package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/localfn"
)

// 流式 fetch：一直有数据就不断（总时长不设上限），连续 fetchIdleWait 没数据才断
func TestLocalFetchStream(t *testing.T) {
	t.Setenv("SHUTTLE_FETCH_ALLOW_PRIVATE", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		gap := 10 * time.Millisecond
		if r.URL.Path == "/stall" {
			gap = 1500 * time.Millisecond
		}
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}))
	defer srv.Close()

	old := fetchIdleWait
	fetchIdleWait = 500 * time.Millisecond // 留够余量：Windows CI 的计时很粗
	defer func() { fetchIdleWait = old }()

	resp, err := localFetch(context.Background(), localfn.FetchRequest{URL: srv.URL + "/ok", Method: "GET", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Stream)
	resp.Stream.Close()
	if err != nil || strings.Count(string(b), "data:") != 5 {
		t.Fatalf("持续有数据应该读完：%q %v", b, err)
	}

	resp, err = localFetch(context.Background(), localfn.FetchRequest{URL: srv.URL + "/stall", Method: "GET", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Stream)
	resp.Stream.Close()
	if err == nil || !strings.Contains(err.Error(), "没收到数据") {
		t.Fatalf("长时间没数据应该断开：%v", err)
	}
}
