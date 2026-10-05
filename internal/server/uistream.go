package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/wsgit"
)

// agent 对话走 Vercel AI SDK 的 UI Message Stream 协议（v1）：前端直接用 useChat，
// 以后换任何兼容这套协议的前端组件都不用改服务端。
//
// 请求：useChat 的标准请求体 {id, messages} 或只带最后一条的 {id, message}。
// 服务端自己保存对话历史（pi session），只取最后一条用户消息的文本。
// 响应：SSE，每行 `data: <chunk JSON>`，最后 `data: [DONE]`。

type uiPart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	URL       string `json:"url,omitempty"`       // file
	Filename  string `json:"filename,omitempty"`  // file
	MediaType string `json:"mediaType,omitempty"` // file
}

type uiMessage struct {
	ID    string   `json:"id"`
	Role  string   `json:"role"`
	Parts []uiPart `json:"parts"`
}

func (m uiMessage) text() string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// images 取出消息里上传到本机的图片路径；别处的地址（data:、http:）不认，界面只会发本机上传的。
func (s *Server) images(m uiMessage) ([]string, error) {
	var out []string
	for _, p := range m.Parts {
		if p.Type != "file" {
			continue
		}
		path := ""
		if name, ok := strings.CutPrefix(p.URL, agent.UploadURLPrefix); ok {
			path = s.agent.UploadPath(name)
		}
		if path == "" {
			return nil, i18n.Errorf("不认识的附件地址：%.80s", "Unrecognized attachment URL: %.80s", p.URL)
		}
		out = append(out, path)
	}
	return out, nil
}

// promptFor 是交给 agent 的文字：带图片时补一行本机路径，agent 要把图放进文章、站点时用得上。
func promptFor(text string, images []string) string {
	if len(images) == 0 {
		return text
	}
	note := "[用户附了 " + fmt.Sprint(len(images)) + " 张图片，本机文件：" + strings.Join(images, "、") + "]"
	if text == "" {
		return note
	}
	return text + "\n\n" + note
}

// apiAgentSteer：POST agent/steer {chat_id, message}，在正在运行的一轮里插话。
// 没在运行返回 409（not_running），界面就当普通消息发。
func (s *Server) apiAgentSteer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatID  string    `json:"chat_id"`
		Message uiMessage `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &req); err != nil || req.Message.ID == "" {
		fail(w, http.StatusBadRequest, i18n.New("要给出 chat_id 和带 id 的 message", "chat_id and a message with an id are required"))
		return
	}
	images, err := s.images(req.Message)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	text := req.Message.text()
	if text == "" && len(images) == 0 {
		fail(w, http.StatusBadRequest, i18n.New("消息是空的", "The message is empty"))
		return
	}
	files := []uiPart{}
	for _, p := range req.Message.Parts {
		if p.Type == "file" {
			files = append(files, p)
		}
	}
	display := map[string]any{"text": text, "files": files}
	if err := s.agent.Steer(req.ChatID, req.Message.ID, promptFor(text, images), images, display); err != nil {
		if errors.Is(err, agent.ErrNotRunning) {
			w.Header().Set("content-type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{"error": i18n.T("这段对话没有在运行", "This chat isn't running"), "not_running": true})
			return
		}
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiAgentChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string            `json:"id"`
		Message  json.RawMessage   `json:"message"`
		Messages []json.RawMessage `json:"messages"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(body, &req); err != nil {
		fail(w, http.StatusBadRequest, i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON"))
		return
	}
	rawLast := req.Message
	if rawLast == nil && len(req.Messages) > 0 {
		rawLast = req.Messages[len(req.Messages)-1]
	}
	var last uiMessage
	if rawLast == nil || json.Unmarshal(rawLast, &last) != nil || last.Role != "user" {
		fail(w, http.StatusBadRequest, i18n.New("最后一条必须是用户消息", "The last message must be a user message"))
		return
	}
	images, err := s.images(last)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if last.text() == "" && len(images) == 0 {
		fail(w, http.StatusBadRequest, i18n.New("消息是空的", "The message is empty"))
		return
	}
	run, err := s.startTurn(req.ID, rawLast, last, images)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, agent.ErrBusy) {
			code = http.StatusConflict
		}
		fail(w, code, err)
		return
	}
	run.serve(r.Context(), w)
}

// startTurn 在 chatID 这段对话里发一条用户消息，后台跑这一轮（和浏览器连接无关），返回这一轮（界面接着推流，手机上轮询快照）。
func (s *Server) startTurn(chatID string, rawLast json.RawMessage, last uiMessage, images []string) (*agentRun, error) {
	if s.agent.Running(chatID) {
		return nil, agent.ErrBusy
	}
	prompt := promptFor(last.text(), images)
	title := last.text()
	if title == "" {
		title = i18n.T("图片", "Image")
	}
	startedAt := time.Now().UnixMilli()
	if err := s.agent.SaveMessage(chatID, rawLast, title, startedAt); err != nil {
		return nil, err
	}

	// 还没起过名的对话（第一句话，或者上次起名失败了）：和这一轮同时让模型起个短标题，这一轮结束前推给界面
	var titled chan string
	if text := last.text(); text != "" && s.agent.NeedsTitle(chatID) {
		titled = make(chan string, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			t, err := s.agent.GenerateTitle(ctx, text)
			if err == nil && t != "" {
				err = s.agent.SetTitle(chatID, t, "auto")
			}
			if err != nil {
				log.Printf("给对话 %s 起名失败：%v", chatID, err)
				t = ""
			}
			titled <- t
		}()
	}

	run := newRun()
	s.runsMu.Lock()
	s.runs[chatID] = run
	s.runsMu.Unlock()
	go s.execRun(context.Background(), chatID, prompt, images, startedAt, run, titled)
	return run, nil
}

// apiAgentResume：AI SDK 断线续传（useChat 的 resume）。有正在进行的一轮就从头回放已产生的分块、
// 再接着推实时的；没有就 204。
func (s *Server) apiAgentResume(w http.ResponseWriter, r *http.Request, chatID string) {
	s.runsMu.Lock()
	run := s.runs[chatID]
	s.runsMu.Unlock()
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	run.serve(r.Context(), w)
}

// execRun 在后台跑完一轮，和浏览器连接无关：刷新、关页面都不会停，停止走 agent/abort。
// ctx 结束也会停（定时任务的超时用它）。返回助手这一轮的回答文字。
func (s *Server) execRun(ctx context.Context, chatID, prompt string, images []string, startedAt int64, run *agentRun, titled <-chan string) (string, error) {
	st := &uiStream{out: run.append, msgID: fmt.Sprintf("a%d", time.Now().UnixNano()), parts: make([]map[string]any, 0)}
	run.mu.Lock()
	run.st = st
	run.mu.Unlock()
	st.send(map[string]any{"type": "start", "messageId": st.msgID, "messageMetadata": map[string]any{"startedAt": startedAt}})

	dir := s.ws.Dir // 跑的过程中切了项目，也提交到这一轮所在的那个
	stats, err := s.agent.Run(ctx, chatID, prompt, images, st.event)
	st.mu.Lock()
	st.closeBlocks()
	st.closeTools(err)
	st.mu.Unlock()
	meta := map[string]any{"startedAt": startedAt, "stats": stats}
	if titled != nil {
		// 回答比起名快时多等一会儿；transient：只给界面更新标题，不进消息
		select {
		case t := <-titled:
			if t != "" {
				st.send(map[string]any{"type": "data-title", "data": map[string]any{"title": t}, "transient": true})
			}
		case <-time.After(5 * time.Second):
		}
	}
	switch {
	case errors.Is(err, agent.ErrAborted):
		meta["aborted"] = true
		st.send(map[string]any{"type": "finish", "finishReason": "stop", "messageMetadata": meta})
	case err != nil:
		meta["error"] = err.Error()
		st.send(map[string]any{"type": "error", "errorText": err.Error()})
		st.send(map[string]any{"type": "finish", "finishReason": "error", "messageMetadata": meta})
	default:
		st.send(map[string]any{"type": "finish", "finishReason": "stop", "messageMetadata": meta})
	}
	run.append("[DONE]")

	// 先落历史再撤掉这一轮：之后来的续传请求拿 204，回答从历史里读
	asst, _ := json.Marshal(map[string]any{"id": st.msgID, "role": "assistant", "parts": st.parts, "metadata": meta})
	if err := s.agent.SaveMessage(chatID, asst, prompt, time.Now().UnixMilli()); err != nil {
		log.Printf("保存对话 %s 失败：%v", chatID, err)
	}
	run.finish()
	s.runsMu.Lock()
	if s.runs[chatID] == run {
		delete(s.runs, chatID)
	}
	s.runsMu.Unlock()

	// 每轮结束把项目的改动提交一次：助手忘了提交，每一轮也能单独回滚（没有 git 就跳过）
	if wsgit.Available() == nil {
		if _, err := wsgit.Commit(dir, commitMessage(prompt)); err != nil {
			log.Printf("自动提交项目失败：%v", err)
		}
	}
	// 回答取最后一步里的文字（前面几步是边调工具边说的过程）
	var answer strings.Builder
	for _, p := range st.parts {
		switch p["type"] {
		case "step-start":
			answer.Reset()
		case "text":
			t, _ := p["text"].(string)
			answer.WriteString(t)
		}
	}
	return strings.TrimSpace(answer.String()), err
}

// commitMessage 取用户这一轮的话的第一行作为提交说明。
func commitMessage(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	if r := []rune(line); len(r) > 60 {
		line = string(r[:60]) + "…"
	}
	if line == "" {
		line = "（图片）"
	}
	return "助手：" + line
}

// agentRun 缓存一轮产生的全部分块，任意多个连接都能从头订阅。
type agentRun struct {
	mu     sync.Mutex
	chunks []string
	done   bool
	wake   chan struct{} // 有新分块或结束时关闭并换新
	st     *uiStream     // 正在拼的助手消息：手机上轮询时取它的快照（remoteai.go）
}

func newRun() *agentRun { return &agentRun{wake: make(chan struct{})} }

func (r *agentRun) append(data string) {
	r.mu.Lock()
	r.chunks = append(r.chunks, data)
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

func (r *agentRun) finish() {
	r.mu.Lock()
	r.done = true
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

// serve 以 SSE 推送：先回放已有分块，再跟着实时推，直到这一轮结束或连接断开。
func (r *agentRun) serve(ctx context.Context, w http.ResponseWriter) {
	w.Header().Set("content-type", "text/event-stream; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	next := 0
	for {
		r.mu.Lock()
		pending := r.chunks[next:]
		next = len(r.chunks)
		done, wake := r.done, r.wake
		r.mu.Unlock()
		for _, c := range pending {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		if flusher != nil && len(pending) > 0 {
			flusher.Flush()
		}
		if done || (len(pending) > 0 && pending[len(pending)-1] == "[DONE]") {
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return // 浏览器断开：只是不再推给它，agent 照常跑
		}
	}
}

// uiStream 把 agent 的扁平事件整理成协议要求的分块：文本和思考要成对的
// *-start / *-end 包起来，工具调用前后要把正在输出的文本块关掉。
type uiStream struct {
	mu      sync.Mutex   // event 和 snapshot 可能在不同的 goroutine
	out     func(string) // 分块输出（写进 agentRun）
	msgID   string
	seq     int
	textID  string
	thinkID string
	inStep  bool

	// 同步拼出的助手消息（UIMessage.parts），一轮结束后存进对话历史
	parts   []map[string]any
	textAt  int
	thinkAt int
	toolAt  map[string]int
}

func (st *uiStream) send(v any) {
	b, _ := json.Marshal(v)
	st.out(string(b))
}

func (st *uiStream) nextID(prefix string) string {
	st.seq++
	return fmt.Sprintf("%s%d", prefix, st.seq)
}

func (st *uiStream) closeText() {
	if st.textID != "" {
		st.send(map[string]any{"type": "text-end", "id": st.textID})
		st.parts[st.textAt]["state"] = "done"
		st.textID = ""
	}
}

func (st *uiStream) closeThink() {
	if st.thinkID != "" {
		st.send(map[string]any{"type": "reasoning-end", "id": st.thinkID})
		st.parts[st.thinkAt]["state"] = "done"
		st.thinkID = ""
	}
}

func (st *uiStream) closeBlocks() {
	st.closeText()
	st.closeThink()
}

// closeTools：这一轮结束时还没拿到结果的工具（被停止、出错、中断），标成失败，
// 界面和存下来的历史都不会一直显示「执行中」。
func (st *uiStream) closeTools(err error) {
	reason := i18n.T("没有执行完", "Didn't finish")
	if errors.Is(err, agent.ErrAborted) {
		reason = i18n.T("已停止", "Stopped")
	}
	for id, i := range st.toolAt {
		if st.parts[i]["state"] != "input-available" {
			continue
		}
		st.send(map[string]any{"type": "tool-output-error", "toolCallId": id, "errorText": reason, "dynamic": true})
		st.parts[i]["state"] = "output-error"
		st.parts[i]["errorText"] = reason
	}
}

// snapshot 是此刻已经拼出来的助手消息（parts 的深拷贝，JSON 形式）。
func (st *uiStream) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, _ := json.Marshal(st.parts)
	var parts []any
	json.Unmarshal(b, &parts)
	return map[string]any{"id": st.msgID, "role": "assistant", "parts": parts}
}

func (st *uiStream) event(e agent.Event) {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch e.Type {
	case "turn_start":
		st.closeBlocks()
		st.send(map[string]any{"type": "start-step"})
		st.parts = append(st.parts, map[string]any{"type": "step-start"})
		st.inStep = true
	case "turn_end":
		st.closeBlocks()
		if st.inStep {
			st.send(map[string]any{"type": "finish-step"})
			st.inStep = false
		}
	case "steer":
		// 插话送达模型：在助手这条消息里插一个 data-steer 片段，界面在这个位置显示用户的话，也跟着存进历史
		st.closeBlocks()
		st.send(map[string]any{"type": "data-steer", "id": e.ToolID, "data": e.Data})
		st.parts = append(st.parts, map[string]any{"type": "data-steer", "id": e.ToolID, "data": e.Data})
	case "context":
		st.send(map[string]any{"type": "message-metadata", "messageMetadata": map[string]any{"live": map[string]any{"contextTokens": e.ContextTokens, "contextWindow": e.ContextWindow}}})
	case "text":
		st.closeThink()
		if st.textID == "" {
			st.textID = st.nextID("t")
			st.send(map[string]any{"type": "text-start", "id": st.textID})
			st.parts = append(st.parts, map[string]any{"type": "text", "text": "", "state": "streaming"})
			st.textAt = len(st.parts) - 1
		}
		st.send(map[string]any{"type": "text-delta", "id": st.textID, "delta": e.Text})
		st.parts[st.textAt]["text"] = st.parts[st.textAt]["text"].(string) + e.Text
	case "thinking":
		st.closeText()
		if st.thinkID == "" {
			st.thinkID = st.nextID("r")
			st.send(map[string]any{"type": "reasoning-start", "id": st.thinkID})
			st.parts = append(st.parts, map[string]any{"type": "reasoning", "text": "", "state": "streaming"})
			st.thinkAt = len(st.parts) - 1
		}
		st.send(map[string]any{"type": "reasoning-delta", "id": st.thinkID, "delta": e.Text})
		st.parts[st.thinkAt]["text"] = st.parts[st.thinkAt]["text"].(string) + e.Text
	case "tool_start":
		st.closeBlocks()
		// 工具是服务端执行的，前端没有定义它们，所以标成 dynamic
		st.send(map[string]any{"type": "tool-input-available", "toolCallId": e.ToolID, "toolName": e.Tool, "input": e.Args, "dynamic": true})
		if st.toolAt == nil {
			st.toolAt = map[string]int{}
		}
		st.parts = append(st.parts, map[string]any{"type": "dynamic-tool", "toolCallId": e.ToolID, "toolName": e.Tool, "state": "input-available", "input": e.Args})
		st.toolAt[e.ToolID] = len(st.parts) - 1
	case "tool_end":
		p := map[string]any{}
		if i, ok := st.toolAt[e.ToolID]; ok {
			p = st.parts[i]
		}
		if e.IsError {
			st.send(map[string]any{"type": "tool-output-error", "toolCallId": e.ToolID, "errorText": e.Text, "dynamic": true})
			p["state"], p["errorText"] = "output-error", e.Text
		} else {
			st.send(map[string]any{"type": "tool-output-available", "toolCallId": e.ToolID, "output": e.Text, "dynamic": true})
			p["state"], p["output"] = "output-available", e.Text
		}
	}
}

// StopRuns 在 Shuttle 退出前调用：中止正在进行的一轮，等它把已产生的部分回复存进历史
// （标记为已停止），最多等 timeout。不这么做的话，进程一退，这一轮的回答就丢了。
func (s *Server) StopRuns(timeout time.Duration) {
	s.agent.Abort("")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.runsMu.Lock()
		n := len(s.runs)
		s.runsMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("退出时仍有 agent 对话没收尾")
}
