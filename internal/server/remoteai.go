package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/tasks"
)

// 手机上用助手（docs/mobile-remote.md「第二步」）：经中转（internal/relay）调到电脑上的这几个内置函数，
// 看对话列表、看一段对话、发消息、停止、跑项目的任务。和页面的 remote 函数走同一条路（站点 Func shuttle.call → ctx.shuttle.call），
// 函数名以 _shuttle. 开头（本机函数的文件名不能以 _ 开头，不会撞名）。新名字 _annulo. 也认（relayRun 里换成 _shuttle.）；
// 报给中转的仍是 _shuttle.：手机端靠找 _shuttle.send 判断能不能用助手，站点 Func 也只放行这个前缀。
//
// 不推流：一轮在电脑上照常跑、照常存；手机每隔几秒调一次 _shuttle.chat，拿到已经存下的消息和正在跑的这一轮此刻的快照。
// 只对电脑上正在打开的那个项目开放（助手的工作目录、对话历史跟着它）。

const remoteAIPrefix = "_shuttle."

var remoteAIFns = []string{"_shuttle.chats", "_shuttle.chat", "_shuttle.send", "_shuttle.abort", "_shuttle.tasks", "_shuttle.task"}

// 回给手机的对话里，一条工具输出最多带多少字（手机只要看个大概，整段输出很大）
const remoteToolOutput = 1500

func (s *Server) relayAI(ctx context.Context, projectID, fn string, input any) (any, error) {
	if !s.ready.Load() || s.ws.Offline || projectID != s.ws.ProjectID {
		return nil, i18n.New("电脑上的 Annulo 现在打开的是别的项目：在电脑上切到这个项目，手机上才能用助手", "Annulo on the computer has another project open: switch to this project on the computer to use the assistant from the phone")
	}
	in, _ := input.(map[string]any)
	str := func(k string) string {
		v, _ := in[k].(string)
		return strings.TrimSpace(v)
	}
	switch fn {
	case "_shuttle.chats":
		list, err := s.agent.ListChats()
		if err != nil {
			return nil, err
		}
		if n := 50; len(list) > n {
			list = list[:n]
		}
		return map[string]any{"list": list}, nil

	case "_shuttle.chat":
		id := str("chat_id")
		c, err := s.agent.LoadChat(id)
		if err != nil {
			return nil, i18n.New("没有这段对话", "No such chat")
		}
		from := 0
		if f, ok := in["from"].(float64); ok && f > 0 {
			from = min(int(f), len(c.Messages))
		}
		msgs := make([]any, 0, len(c.Messages)-from)
		for _, m := range c.Messages[from:] {
			msgs = append(msgs, slimMessage(m))
		}
		out := map[string]any{"id": c.ID, "title": c.Title, "total": len(c.Messages), "from": from, "messages": msgs, "status": s.agent.ChatStatuses()[id]}
		s.runsMu.Lock()
		run := s.runs[id]
		s.runsMu.Unlock()
		if run != nil {
			run.mu.Lock()
			st := run.st
			run.mu.Unlock()
			if st != nil {
				out["live"] = slimMessage(mustMarshal(st.snapshot()))
			}
		}
		return out, nil

	case "_shuttle.send":
		text := str("text")
		if text == "" {
			return nil, i18n.New("消息是空的", "The message is empty")
		}
		id := str("chat_id")
		if id == "" {
			id = fmt.Sprintf("m%d", time.Now().UnixMilli())
		}
		msgID := fmt.Sprintf("u%d", time.Now().UnixNano())
		// 正在跑：当插话送进去（外部 agent 不支持插话，报忙）
		if s.agent.Running(id) {
			display := map[string]any{"text": text, "files": []any{}}
			if err := s.agent.Steer(id, msgID, text, nil, display); err != nil {
				return nil, agent.ErrBusy
			}
			return map[string]any{"chat_id": id, "steered": true}, nil
		}
		last := uiMessage{ID: msgID, Role: "user", Parts: []uiPart{{Type: "text", Text: text}}}
		raw, _ := json.Marshal(last)
		if _, err := s.startTurn(id, raw, last, nil); err != nil {
			return nil, err
		}
		return map[string]any{"chat_id": id}, nil

	case "_shuttle.abort":
		s.agent.Abort(str("chat_id"))
		return map[string]any{"ok": true}, nil

	case "_shuttle.tasks":
		out := []map[string]any{}
		for _, t := range tasks.List(s.ws.Dir) {
			info := s.taskInfo(t)
			out = append(out, map[string]any{"id": t.ID, "name": t.Name, "description": t.Description, "running": info.Running, "last": info.Last})
		}
		return map[string]any{"list": out}, nil

	case "_shuttle.task":
		t, err := tasks.Load(s.ws.Dir, str("id"))
		if err != nil {
			return nil, err
		}
		start := time.Now()
		chatID := fmt.Sprintf("task-%s-%d", t.ID, start.UnixMilli())
		run, err := s.claimTask(t, in["input"], chatID, false, start)
		if err != nil {
			return nil, err
		}
		go s.finishTask(t, run, "")
		return map[string]any{"chat_id": chatID}, nil
	}
	return nil, i18n.Errorf("没有这个函数：%s", "No such function: %s", fn)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// slimMessage 去掉手机用不上的大块内容：工具的输入输出截短，思考过程只留开头。
func slimMessage(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return map[string]any{}
	}
	parts, _ := m["parts"].([]any)
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"output", "errorText", "text"} {
			if v, ok := pm[k].(string); ok {
				limit := remoteToolOutput
				if k == "text" && pm["type"] == "text" {
					continue // 回答原文不截
				}
				if r := []rune(v); len(r) > limit {
					pm[k] = string(r[:limit]) + "…"
				}
			}
		}
		if in, ok := pm["input"]; ok {
			if b, _ := json.Marshal(in); len(b) > remoteToolOutput && pm["toolName"] != "request_user_input" {
				r := []rune(string(b))
				pm["input"] = map[string]any{"summary": string(r[:min(len(r), remoteToolOutput/2)]) + "…"}
			}
		}
	}
	return m
}
