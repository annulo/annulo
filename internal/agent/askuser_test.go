package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRequestUserInputEndsTurn(t *testing.T) {
	tool := (&Agent{}).requestUserInputTool()
	ok := map[string]any{"title": "定位", "questions": []any{map[string]any{"id": "who", "type": "short_text", "label": "卖给谁"}}}
	r, err := tool.Execute(context.Background(), "c1", ok, nil)
	if err != nil || !r.Terminate || !strings.Contains(resultText(r), "waiting_for_user") {
		t.Fatalf("合法问卷：应该显示出来并结束这一轮：%+v %v", r, err)
	}
	// 不合法的退回给模型改，这一轮继续
	bad := map[string]any{"questions": []any{map[string]any{"id": "x", "type": "single_select", "label": "选一个"}}}
	r, err = tool.Execute(context.Background(), "c2", bad, nil)
	if err != nil || r.Terminate || !strings.Contains(resultText(r), "INVALID_ARGUMENT") {
		t.Fatalf("不合法问卷：应该退回、不结束：%+v %v", r, err)
	}
}
