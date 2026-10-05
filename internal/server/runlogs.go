package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var runLogID = regexp.MustCompile(`^r[0-9]+$`)

func (s *Server) apiRunLogs(w http.ResponseWriter, r *http.Request) {
	if s.ws == nil {
		writeJSON(w, map[string]any{"list": []any{}})
		return
	}
	dir := filepath.Join(s.cfg.Dir, "logs", "local", s.ws.ProjectID)
	if id := r.URL.Query().Get("id"); id != "" {
		if !runLogID.MatchString(id) {
			http.Error(w, "invalid run id", 400)
			return
		}
		f, err := os.Open(filepath.Join(dir, id+".jsonl"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/x-ndjson")
		http.ServeContent(w, r, id, time.Time{}, f)
		return
	}
	entries, _ := os.ReadDir(dir)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	list := []map[string]any{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" || !runLogID.MatchString(strings.TrimSuffix(entry.Name(), ".jsonl")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
		if len(lines) == 0 {
			continue
		}
		var start, end struct {
			At   string         `json:"at"`
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(lines[0], &start) != nil {
			continue
		}
		if fn := r.URL.Query().Get("fn"); fn != "" && start.Data["fn"] != fn {
			continue
		}
		json.Unmarshal(lines[len(lines)-1], &end)
		list = append(list, map[string]any{"id": start.Data["run_id"], "fn": start.Data["fn"], "started_at": start.At, "status": end.Type, "ms": end.Data["ms"], "log_file": filepath.Join(dir, entry.Name())})
		if len(list) >= limit {
			break
		}
	}
	writeJSON(w, map[string]any{"list": list})
}
