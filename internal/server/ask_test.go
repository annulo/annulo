package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalAskValidates(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{"text":"   "}`, `not json`} {
		w := httptest.NewRecorder()
		s.apiLocalAsk(w, httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/ask", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code %d", body, w.Code)
		}
	}
}
