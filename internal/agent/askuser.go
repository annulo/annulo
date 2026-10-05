package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	piagent "github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// request_user_input：照搬 talizen 平台的问卷工具。
// agent 调它时对话面板显示问卷卡片，这一轮就此结束（工具结果带 Terminate），不占着助手、界面不显示运行中；
// 用户在卡片上提交，或者直接在输入框打字，都作为下一条消息发给助手，它接着往下做。重启 Shuttle 也不影响回答。
// 平台还支持 image_upload，这里先不支持。

type userInputOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type userInputQuestion struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Placeholder string            `json:"placeholder,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Options     []userInputOption `json:"options,omitempty"`
	AllowCustom bool              `json:"allow_custom,omitempty"`
}

type userInputParams struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Questions   []userInputQuestion `json:"questions"`
}

const requestUserInputSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "Short heading for the questionnaire"},
    "description": {"type": "string", "description": "One sentence explaining how the answers improve the result"},
    "questions": {
      "type": "array",
      "description": "Ask only high-impact missing questions, normally 1-6",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "Stable snake_case answer key"},
          "type": {"type": "string", "enum": ["single_select", "multi_select", "short_text", "long_text"], "description": "Question input type"},
          "label": {"type": "string", "description": "Question shown to the user"},
          "description": {"type": "string", "description": "Optional context explaining why this matters"},
          "placeholder": {"type": "string", "description": "Optional hint shown inside text or custom-answer inputs"},
          "required": {"type": "boolean"},
          "options": {
            "type": "array",
            "description": "Options for select questions; use 2-6 concise choices",
            "items": {
              "type": "object",
              "properties": {
                "value": {"type": "string", "description": "Stable option value; defaults to the label"},
                "label": {"type": "string", "description": "Short user-facing option label"},
                "description": {"type": "string", "description": "Optional explanation of the option"}
              },
              "required": ["label"]
            }
          },
          "allow_custom": {"type": "boolean", "description": "Whether the user may enter a custom answer"}
        },
        "required": ["id", "type", "label"]
      }
    }
  },
  "required": ["questions"]
}`

// validateUserInput 和平台一样：所有问题一次报完，title 缺了不算错。
func validateUserInput(p userInputParams) error {
	var problems []string
	if len(p.Questions) == 0 || len(p.Questions) > 6 {
		problems = append(problems, "questions must contain 1-6 items")
	}
	seen := map[string]bool{}
	for i, q := range p.Questions {
		id := strings.TrimSpace(q.ID)
		switch {
		case id == "":
			problems = append(problems, fmt.Sprintf("questions[%d].id is required", i))
		case seen[id]:
			problems = append(problems, fmt.Sprintf("questions[%d].id %q is duplicated", i, id))
		}
		seen[id] = true
		if strings.TrimSpace(q.Label) == "" {
			problems = append(problems, fmt.Sprintf("questions[%d].label is required", i))
		}
		switch q.Type {
		case "single_select", "multi_select":
			if len(q.Options) < 2 || len(q.Options) > 6 {
				problems = append(problems, fmt.Sprintf("questions[%d].options must contain 2-6 items", i))
			}
			for j, o := range q.Options {
				// value 模型常漏写，缺了就用 label（界面同样这么补），不为这个让它重发一轮
				if strings.TrimSpace(o.Label) == "" {
					problems = append(problems, fmt.Sprintf("questions[%d].options[%d] requires a label", i, j))
				}
			}
		case "short_text", "long_text":
		default:
			problems = append(problems, fmt.Sprintf("questions[%d].type %q is unsupported (use single_select, multi_select, short_text or long_text)", i, q.Type))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func toolJSON(v any) piagent.AgentToolResult {
	b, _ := json.Marshal(v)
	return piagent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(b)}}}
}

const requestUserInputName = "request_user_input"

func (a *Agent) requestUserInputTool() piagent.AgentTool {
	var params ai.Schema
	json.Unmarshal([]byte(requestUserInputSchema), &params)
	return piagent.AgentTool{
		Name:  requestUserInputName,
		Label: "向用户提问",
		Description: "Pause and ask a small set of high-impact clarification questions (single/multi select or text). " +
			"Write all user-visible text in the same language as the user's latest request (Chinese in, Chinese out), while preserving brand names and technical terms; ids and option values may use snake_case. " +
			"Call this tool alone, and do not use it for minor ambiguities. " +
			"The questionnaire is shown and your turn ends right away (result {\"status\": \"waiting_for_user\"}); the user's answers arrive as their next message.",
		Parameters: &params,
		Execute: func(ctx context.Context, id string, p map[string]any, _ piagent.ToolUpdateFunc) (piagent.AgentToolResult, error) {
			b, _ := json.Marshal(p)
			var in userInputParams
			if err := json.Unmarshal(b, &in); err != nil {
				return toolJSON(map[string]any{"ok": false, "error_code": "INVALID_ARGUMENT", "error": "invalid arguments: " + err.Error()}), nil
			}
			if err := validateUserInput(in); err != nil {
				return toolJSON(map[string]any{"ok": false, "error_code": "INVALID_ARGUMENT", "error": "request_user_input was not shown because its questionnaire is invalid: " + err.Error() + ". Correct the arguments and call request_user_input again."}), nil
			}
			// 问卷已经显示出来：这一轮到此为止，回答作为用户的下一条消息到来
			r := toolJSON(map[string]any{"status": "waiting_for_user", "note": "The questionnaire is shown to the user. Stop here; their answers will come as the next user message."})
			r.Terminate = true
			return r, nil
		},
	}
}
