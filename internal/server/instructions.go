package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/wsgit"
)

// 给助手的说明：当前项目的 INSTRUCTIONS.md（设置 → 项目里编辑），拼进助手的系统提示，新对话生效。

const maxInstructions = 8000 // 和系统提示里截断的长度一致（按字数）

// apiInstructionsGet：GET project/instructions → {text, file}
func (s *Server) apiInstructionsGet(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(s.ws.Dir, agent.InstructionsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"text": string(b), "file": agent.InstructionsFile, "max": maxInstructions})
}

// apiInstructionsPut：PUT project/instructions {text}。写空就删掉文件；改了在项目的 git 里提交一次，能回滚。
func (s *Server) apiInstructionsPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err := json.Unmarshal(b, &in); err != nil {
		fail(w, http.StatusBadRequest, i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON"))
		return
	}
	text := strings.TrimSpace(in.Text)
	if n := len([]rune(text)); n > maxInstructions {
		fail(w, http.StatusBadRequest, i18n.New("太长了：最多 8000 字", "Too long: at most 8000 characters"))
		return
	}
	dir := s.ws.Dir
	p := filepath.Join(dir, agent.InstructionsFile)
	var err error
	if text == "" {
		if err = os.Remove(p); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = os.WriteFile(p, []byte(text+"\n"), 0o644)
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if wsgit.Available() == nil {
		if _, err := wsgit.Commit(dir, "修改给助手的说明（"+agent.InstructionsFile+"）"); err != nil {
			log.Printf("提交 %s 失败：%v", agent.InstructionsFile, err)
		}
	}
	s.apiInstructionsGet(w, r)
}
