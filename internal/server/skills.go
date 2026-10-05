package server

import (
	"context"

	"github.com/annulo/annulo/internal/i18n"

	agentpkg "github.com/annulo/annulo/internal/agent"

	"encoding/json"
	"io"
	"net/http"
	"time"
)

func (s *Server) apiSkills(w http.ResponseWriter, r *http.Request) {
	active, local := s.agent.ListSkills()
	if active == nil {
		active = []agentpkg.SkillInfo{}
	}
	if local == nil {
		local = []agentpkg.SkillInfo{}
	}
	writeJSON(w, map[string]any{"skills": active, "local": local})
}

func (s *Server) apiSkillInstall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
		Link   bool   `json:"link"` // 导入本机其他 agent 的 skill 时软链过来，跟着源目录更新
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err := json.Unmarshal(b, &in); err != nil {
		fail(w, http.StatusBadRequest, i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	installed, err := s.agent.InstallSkill(ctx, in.Source, in.Link)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"installed": installed})
}

func (s *Server) apiSkillToggle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err := json.Unmarshal(b, &in); err != nil || in.Name == "" {
		fail(w, http.StatusBadRequest, i18n.New("要给出 name 和 enabled", "name and enabled are required"))
		return
	}
	if err := s.agent.SetSkillEnabled(in.Name, in.Enabled); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.apiSkills(w, r)
}

func (s *Server) apiSkillRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.agent.RemoveSkill(r.URL.Query().Get("name")); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiSkills(w, r)
}
