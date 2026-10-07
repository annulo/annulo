package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/tasks"
	"github.com/annulo/annulo/internal/wsgit"
)

// 项目的任务（tasks/<id>.md，见 internal/tasks）：页面上的按钮或定时任务按它开一段对话交给助手，过程在对话里看得见。
// 同一个任务可以同时跑几次（参数不同，比如写不同的选题）；同一个任务、同样的参数正在跑时不重复开。
// 最近一次的结果存在本机 ~/.shuttle/workspaces/<项目>/tasks.json，不进项目的 git。

const maxTaskBody = 20000 // 任务说明最多多少字

type taskRun struct {
	Task      string    `json:"task"`
	ChatID    string    `json:"chat_id"`
	Input     any       `json:"input,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Scheduled bool      `json:"scheduled,omitempty"` // 定时任务发起的
	Ms        int64     `json:"ms,omitempty"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Result    string    `json:"result,omitempty"` // 助手最后的回答，截到 500 字
	ws        string
	inputKey  string
}

type taskRunner struct {
	mu      sync.Mutex
	running map[string]*taskRun // 对话 id → 正在跑的
	last    map[string]*taskRun // 任务 id → 最近跑完的一次
	lastOf  string              // last 属于哪个项目
}

func (s *Server) taskStateFile() string {
	return filepath.Join(s.cfg.Dir, "workspaces", s.ws.ProjectID, "tasks.json")
}

// syncTaskState 确保 last 是当前项目的。调用方持有锁。
func (s *Server) syncTaskState() {
	tr := &s.tasks
	if tr.lastOf == s.ws.ProjectID && tr.last != nil {
		return
	}
	tr.lastOf, tr.last = s.ws.ProjectID, map[string]*taskRun{}
	if b, err := os.ReadFile(s.taskStateFile()); err == nil {
		json.Unmarshal(b, &tr.last)
	}
}

func taskNote(name string, scheduled bool) string {
	if scheduled {
		return fmt.Sprintf(scheduledNote(), name)
	}
	return fmt.Sprintf(i18n.T("（这是项目里的任务「%s」，用户在页面上点按钮发起的。它在后台跑，用户不一定在看：不要用 request_user_input 等用户回答，缺的信息按合理的默认做完，并在最后的回复里说明做了哪些假设、有什么要用户处理的。）\n\n",
		"(This is the project task \"%s\", started by the user from a button on a project page. It runs in the background and the user may not be watching: don't use request_user_input to wait for answers; fill in missing details with sensible defaults, and in your final reply list the assumptions you made and anything the user needs to handle.)\n\n"), name)
}

// claimTask 占住「这个任务、这组参数」：同样的正在跑就报错。
func (s *Server) claimTask(t *tasks.Task, input any, chatID string, scheduled bool, start time.Time) (*taskRun, error) {
	b, _ := json.Marshal(input)
	key := t.ID + "\x00" + string(b)
	tr := &s.tasks
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.running == nil {
		tr.running = map[string]*taskRun{}
	}
	for _, r := range tr.running {
		if r.ws == s.ws.ProjectID && r.inputKey == key {
			return nil, i18n.Errorf("「%s」正在跑（同样的参数），等它跑完", "\"%s\" is already running with the same input; wait for it to finish", t.Name)
		}
	}
	r := &taskRun{Task: t.ID, ChatID: chatID, Input: input, StartedAt: start, Scheduled: scheduled, ws: s.ws.ProjectID, inputKey: key}
	tr.running[chatID] = r
	return r, nil
}

// execTask 按任务说明开一段对话交给助手，跑完才返回（调用方自己决定是不是放到后台）。
func (s *Server) execTask(id string, input any, chatID, fallbackName string, scheduled bool, start time.Time) (string, error) {
	t, err := tasks.Load(s.ws.Dir, id)
	if err != nil {
		return "", err
	}
	r, err := s.claimTask(t, input, chatID, scheduled, start)
	if err != nil {
		return "", err
	}
	return s.startTask(t, r, fallbackName)()
}

// startTask 先把任务的对话存好（返回 chat_id 前页面就可能去打开它），返回跑完任务、记下结果的函数（调用方放到后台）。
func (s *Server) startTask(t *tasks.Task, r *taskRun, fallbackName string) func() (string, error) {
	name := t.Name
	if name == t.ID && fallbackName != "" {
		name = fallbackName
	}
	prompt := taskNote(name, r.Scheduled) + taskPrompt(t, r.Input)
	title := i18n.T("任务：", "Task: ") + name
	if r.Scheduled {
		title = i18n.T("定时：", "Scheduled: ") + name
	}
	if t.Thinking != "" {
		s.agent.SetChatThinking(r.ChatID, t.Thinking) // 任务固定的思考档位，只管任务跑的这一轮
	}
	exec, err := s.beginBackgroundChat(r.ChatID, title, prompt, r.StartedAt)
	return func() (string, error) {
		answer := ""
		if err == nil {
			answer, err = exec()
		}
		s.agent.SetChatThinking(r.ChatID, "") // 跑完了：用户接着聊就按自己的设置
		return s.recordTask(t, r, answer, err)
	}
}

// recordTask 记下任务这一次的结果，从正在跑的里去掉。
func (s *Server) recordTask(t *tasks.Task, r *taskRun, answer string, err error) (string, error) {
	r.Ms, r.OK, r.Result = time.Since(r.StartedAt).Milliseconds(), err == nil, truncateRunes(answer, 500)
	if err != nil {
		r.Error = err.Error()
		log.Printf("任务 %s（%s）失败：%v", t.ID, r.ChatID, err)
	}
	tr := &s.tasks
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.running, r.ChatID)
	if r.ws == s.ws.ProjectID { // 跑的时候切了项目：结果不记到别的项目上
		s.syncTaskState()
		tr.last[t.ID] = r
		p := s.taskStateFile()
		os.MkdirAll(filepath.Dir(p), 0o700)
		b, _ := json.MarshalIndent(tr.last, "", "  ")
		os.WriteFile(p, b, 0o600)
	}
	return answer, err
}

// taskPrompt：不把整份说明贴进对话，让助手自己去读文件（对话里看得到它读了哪份、总是最新的那份）。
func taskPrompt(t *tasks.Task, input any) string {
	p := i18n.T("按项目里的任务「%s」做：先读 %s，照里面的说明做完。", "Do the project task \"%s\": read %s first and follow it to the end.")
	out := fmt.Sprintf(p, t.Name, t.Path)
	if t.PromptFile != "" {
		out += fmt.Sprintf(i18n.T("用户对怎么写的要求在 %s，照它做；和任务说明里的规则冲突时，以规则为准。",
			" The user's requirements for how to write it are in %s — follow them; where they conflict with the rules in the task file, the rules win."),
			filepath.Join(filepath.Dir(filepath.Dir(t.Path)), t.PromptFile)) // 和任务文件一样给绝对路径
	}
	if input != nil {
		b, _ := json.Marshal(input)
		if s := string(b); s != "null" && s != "{}" {
			out += i18n.T("\n\n这次的参数：", "\n\nInput for this run: ") + s
		}
	}
	return out
}

type taskInfo struct {
	*tasks.Task
	Running []*taskRun `json:"running"`
	Last    *taskRun   `json:"last,omitempty"`
}

func (s *Server) taskInfo(t *tasks.Task) taskInfo {
	tr := &s.tasks
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s.syncTaskState()
	in := taskInfo{Task: t, Running: []*taskRun{}, Last: tr.last[t.ID]}
	for _, r := range tr.running {
		if r.ws == s.ws.ProjectID && r.Task == t.ID {
			in.Running = append(in.Running, r)
		}
	}
	sort.Slice(in.Running, func(i, j int) bool { return in.Running[i].StartedAt.Before(in.Running[j].StartedAt) })
	return in
}

// GET  local/tasks              项目的任务：[{ id, name, description, file, body, running: [...], last }]
// GET  local/tasks/<id>         一个任务
// PUT  local/tasks/<id>         { body } 改说明正文（frontmatter 不动）；{ prompt } 存用户改的「怎么写」（user/prompts/<id>.md）；
//
//	{ reset_prompt: true } 删掉用户改的、回到模板默认（prompts/<id>.md）。都在项目的 git 里提交一次
//
// POST local/tasks/<id>/run     { input? } 开一段对话交给助手跑（后台跑），马上返回 { chat_id, task }
func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request, rest string) {
	if rest == "" && r.Method == http.MethodGet {
		out := []taskInfo{}
		for _, t := range tasks.List(s.ws.Dir) {
			out = append(out, s.taskInfo(t))
		}
		writeJSON(w, map[string]any{"list": out})
		return
	}
	id, action := splitNameAction(s.ws.Dir, rest)
	t, err := tasks.Load(s.ws.Dir, id)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		writeJSON(w, s.taskInfo(t))
	case action == "" && r.Method == http.MethodPut:
		var in struct {
			Body        string  `json:"body"`
			Prompt      *string `json:"prompt"`
			ResetPrompt bool    `json:"reset_prompt"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 512<<10))
		if err := json.Unmarshal(b, &in); err != nil {
			fail(w, http.StatusBadRequest, i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON"))
			return
		}
		if in.Prompt != nil || in.ResetPrompt {
			// 用户的「怎么写」：可以清空（留一个空文件，页面上仍然能再写）
			var err error
			msg := "修改任务的写法（" + tasks.UserPromptRel(t.ID) + "）"
			switch {
			case in.ResetPrompt:
				err, msg = t.ResetPrompt(), "任务的写法恢复默认（"+t.ID+"）"
			case len([]rune(*in.Prompt)) > maxTaskBody:
				fail(w, http.StatusBadRequest, i18n.Errorf("太长了：最多 %d 字", "Too long: at most %d characters", maxTaskBody))
				return
			default:
				err = t.SavePrompt(*in.Prompt)
			}
			if err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
			if wsgit.Available() == nil {
				if _, err := wsgit.Commit(s.ws.Dir, msg); err != nil {
					log.Printf("提交任务写法失败：%v", err)
				}
			}
			writeJSON(w, s.taskInfo(t))
			return
		}
		if strings.TrimSpace(in.Body) == "" {
			fail(w, http.StatusBadRequest, i18n.New("说明不能是空的", "The instructions can't be empty"))
			return
		}
		if len([]rune(in.Body)) > maxTaskBody {
			fail(w, http.StatusBadRequest, i18n.Errorf("太长了：最多 %d 字", "Too long: at most %d characters", maxTaskBody))
			return
		}
		if err := t.SaveBody(in.Body); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		if wsgit.Available() == nil {
			if _, err := wsgit.Commit(s.ws.Dir, "修改任务说明（"+t.File+"）"); err != nil {
				log.Printf("提交 %s 失败：%v", t.File, err)
			}
		}
		writeJSON(w, s.taskInfo(t))
	case action == "run" && r.Method == http.MethodPost:
		var in struct {
			Input any `json:"input"`
		}
		if r.ContentLength != 0 {
			if err := readJSON(r, &in); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
		}
		start := time.Now()
		chatID := fmt.Sprintf("task-%s-%d", plugin.ChatKey(t.ID), start.UnixMilli())
		run, err := s.claimTask(t, in.Input, chatID, false, start)
		if err != nil {
			fail(w, http.StatusConflict, err)
			return
		}
		// startTask 在这里同步跑完（go 语句先在当前 goroutine 求出函数值），存好对话再返回 chat_id；返回的函数才放到后台
		go s.startTask(t, run, "")()
		writeJSON(w, map[string]any{"chat_id": chatID, "task": s.taskInfo(t)})
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个操作：%s", "No such action: %s", action))
	}
}

// apiLocalAsk：页面上的「交给助手」。POST local/ask { text, title? } 新开一段对话把这句话交给助手（后台跑），马上返回 { chat_id }；
// 页面在右侧打开那段对话（shuttle:open-chat），轮询 agent/running 看它跑完没有。和任务一样，只是没有任务文件。
func (s *Server) apiLocalAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		fail(w, http.StatusBadRequest, i18n.New("要交给助手的话是空的", "Nothing to hand to the assistant"))
		return
	}
	if len([]rune(text)) > maxTaskBody {
		fail(w, http.StatusBadRequest, i18n.Errorf("太长了：最多 %d 字", "Too long: at most %d characters", maxTaskBody))
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = truncateRunes(strings.SplitN(text, "\n", 2)[0], 30)
	}
	start := time.Now()
	chatID := fmt.Sprintf("ask-%d", start.UnixMilli())
	exec, err := s.beginBackgroundChat(chatID, title, text, start)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	go func() {
		if _, err := exec(); err != nil {
			log.Printf("交给助手（%s）失败：%v", chatID, err)
		}
	}()
	writeJSON(w, map[string]any{"chat_id": chatID})
}
