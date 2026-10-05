package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// 模型上下文（pi 会话文件）和界面历史（Shuttle 的 JSONL）是两份记录。pi 的会话文件要等第一条
// 助手回复完成才落盘，进程在这之前被杀，界面上有提问、模型却不知道。恢复会话时对一次账：
// pi 覆盖不了界面历史里的全部提问，就用界面历史重建上下文，保证模型看到的和用户看到的一致。

type uiMsg struct {
	Role  string `json:"role"`
	Parts []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ToolName  string          `json:"toolName"`
		State     string          `json:"state"`
		Input     json.RawMessage `json:"input"`
		Output    json.RawMessage `json:"output"`
		ErrorText string          `json:"errorText"`
		URL       string          `json:"url"`
		Data      struct {
			Text string `json:"text"`
		} `json:"data"` // data-steer：用户中途插的话
	} `json:"parts"`
	Metadata struct {
		Aborted bool   `json:"aborted"`
		Error   string `json:"error"`
	} `json:"metadata"`
}

func countUsers(msgs []agent.AgentMessage) int {
	n := 0
	for _, m := range msgs {
		switch m.(type) {
		case ai.UserMessage, *ai.UserMessage:
			n++
		}
	}
	return n
}

// historyFromUI 把界面历史转成 pi 的消息：用户消息取文本；助手消息取文本，工具调用折成一行摘要
// （原始调用结构没存，模型只需要知道做过什么、结果如何）。
func historyFromUI(raw []json.RawMessage, model *ai.Model) []agent.AgentMessage {
	var out []agent.AgentMessage
	for _, r := range raw {
		var m uiMsg
		if json.Unmarshal(r, &m) != nil {
			continue
		}
		var sb strings.Builder
		assistant := func(text string) {
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, ai.AssistantMessage{
					Content: ai.ContentList{ai.TextContent{Text: text}},
					Api:     model.Api, Provider: model.Provider, Model: model.ID,
					StopReason: ai.StopStop,
				})
			}
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "data-steer":
				// 助手回复中间用户插的话：前面的算一条助手消息，插话是一条用户消息，后面接着算助手的
				if m.Role == "assistant" && strings.TrimSpace(p.Data.Text) != "" {
					assistant(sb.String())
					sb.Reset()
					out = append(out, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: p.Data.Text}}})
				}
			case "text":
				sb.WriteString(p.Text)
				sb.WriteString("\n")
			case "file":
				// 重建的上下文不再带图片本身，留个说明和本机路径
				fmt.Fprintf(&sb, "[图片：%s]\n", filepath.Join("~/.annulo/uploads", strings.TrimPrefix(p.URL, UploadURLPrefix)))
			case "dynamic-tool":
				in := string(p.Input)
				if len(in) > 200 {
					in = in[:200] + "…"
				}
				res := string(p.Output)
				if p.State == "output-error" {
					res = "失败：" + p.ErrorText
				}
				if len(res) > 300 {
					res = res[:300] + "…"
				}
				fmt.Fprintf(&sb, "[调用工具 %s %s → %s]\n", p.ToolName, in, res)
			}
		}
		text := strings.TrimSpace(sb.String())
		switch m.Role {
		case "user":
			if text != "" {
				out = append(out, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
			}
		case "assistant":
			switch {
			case m.Metadata.Aborted:
				text += "\n（这次回复被中断了）"
			case m.Metadata.Error != "":
				text += "\n（这次回复出错：" + m.Metadata.Error + "）"
			}
			assistant(text)
		}
	}
	// 连续两条用户消息之间补一条说明：前一条因为中断没得到回复
	var fixed []agent.AgentMessage
	for i, m := range out {
		fixed = append(fixed, m)
		_, isUser := m.(ai.UserMessage)
		if isUser && i+1 < len(out) {
			if _, nextUser := out[i+1].(ai.UserMessage); nextUser {
				fixed = append(fixed, ai.AssistantMessage{
					Content: ai.ContentList{ai.TextContent{Text: "（这条消息没来得及回复：服务中途重启了）"}},
					Api:     model.Api, Provider: model.Provider, Model: model.ID, StopReason: ai.StopStop,
				})
			}
		}
	}
	return fixed
}
