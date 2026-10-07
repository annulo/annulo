package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/tasks"
)

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 本机定时：运营后台在 schedules/ 目录里声明「跑什么、多久跑一次」，一个任务一个文件，文件名就是它的 id，Shuttle 开着就按时跑。
// 跑的可以是本机函数（fn），也可以是给助手的一段提示（prompt）：
//
//	schedules/leads.sync.json      { "fn": "leads.sync", "every": "30m", "name": "拉取表单询盘" }
//	schedules/review.check.json    { "fn": "review.check", "at": "09:00", "name": "改站复查", "input": { "days": 14 } }
//	schedules/weekly-report.json   { "task": "weekly-report", "every": "7d", "name": "写周报" }
//	schedules/daily-check.json     { "prompt": "看一眼昨天的数据…", "at": "09:00", "name": "每日检查" }
//
// id（文件名）是运行记录和暂停状态的 key：同一个函数配多条（不同 input）就用不同的文件名。
//
// task：跑项目里的任务（tasks/<id>.md，见 tasks.go），和后台页面上点按钮跑的是同一份说明、同一份运行记录。
// prompt：到点开一段新对话把这段话交给助手，和用户自己发的一样（同样的工具、skill，结束自动提交项目），
// 对话出现在对话列表里、跑完标成有新回复。用户不在场：助手不要等问卷回答，最长跑 promptRunTimeout。
// prompt、task 的 id 会拼进对话 id，只能用字母、数字、- 和 _。确定的活写成本机函数，只有要判断、要写的才用 prompt。
//
// every 是间隔（最短 5m）；at 是每天几点（本机时区）。两个都写时按 at。
// 电脑关着、Shuttle 没开时不跑；再打开时过了点的任务补跑一次（不会把错过的每一次都补上）。
// 运行记录和「暂停」存在本机 ~/.shuttle/workspaces/<工作空间>/schedules.json，不进工作空间的 git。
// 和 GEO 周测、GSC 同步、社媒采集这些业务无关：跑哪个函数、多久跑一次由工作空间自己定。

// WorkspaceSchedulesDir 是运营后台自己声明的定时任务（一个任务一个 <id>.json）。
const WorkspaceSchedulesDir = "schedules"

// legacySchedulesFile 是老格式（一个大数组）：不再读，只用来提示项目要升级模板。
const legacySchedulesFile = "shuttle.schedules.json"

const minEvery = 5 * time.Minute

// promptRunTimeout：定时交给助手的一轮最长跑多久，到了就停（用户不在场，卡在问卷上也会停）
const promptRunTimeout = 30 * time.Minute

// scheduledNote 接在定时提示前面：告诉助手用户不在场。按界面语言出（这段话也显示在对话里）
func scheduledNote() string {
	// 不再写对话 id：要记「出自哪段对话」的，本机函数自己读 ctx.chat_id（annulo run 带过去），不用助手照抄
	return i18n.T("（这是定时任务「%s」自动发起的。用户现在不在：不要用 request_user_input 等用户回答，缺的信息按合理的默认做完，并在最后的回复里说明做了哪些假设、有什么要用户处理的。）\n\n",
		"(This chat was started automatically by the scheduled task \"%s\". The user isn't here: don't use request_user_input to wait for answers; fill in missing details with sensible defaults, and in your final reply list the assumptions you made and anything the user needs to handle.)\n\n")
}

var atRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// chatKeyRe：prompt 任务的 id 会拼进对话 id，只收对话 id 认的字符
var chatKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type scheduleDef struct {
	ID     string `json:"-"` // 文件名（schedules/<id>.json）
	Fn     string `json:"fn,omitempty"`
	Prompt string `json:"prompt,omitempty"` // 给助手的一段话；fn / prompt / task 三选一
	Task   string `json:"task,omitempty"`   // 项目里的任务 tasks/<id>.md
	Name   string `json:"name,omitempty"`
	Every  string `json:"every,omitempty"`
	At     string `json:"at,omitempty"`
	Input  any    `json:"input,omitempty"`

	every time.Duration
}

func (d *scheduleDef) key() string {
	if d.ID != "" {
		return d.ID
	}
	if d.Task != "" {
		return d.Task
	}
	return d.Fn
}

// next 算下一次该跑的时间。last 是上次开始跑的时间（没跑过是零值）。
func (d *scheduleDef) next(last, now time.Time) time.Time {
	if d.At != "" {
		m := atRe.FindStringSubmatch(d.At)
		var h, mi int
		fmt.Sscanf(m[1]+" "+m[2], "%d %d", &h, &mi)
		today := time.Date(now.Year(), now.Month(), now.Day(), h, mi, 0, 0, now.Location())
		// 今天的点已经过了、今天还没跑过：现在就补跑
		if !today.After(now) && last.Before(today) {
			return today
		}
		if today.After(now) {
			return today
		}
		return today.AddDate(0, 0, 1)
	}
	if last.IsZero() {
		return now
	}
	return last.Add(d.every)
}

type scheduleRun struct {
	StartedAt time.Time `json:"started_at"`
	Ms        int64     `json:"ms"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Result    string    `json:"result,omitempty"`  // 返回值的 JSON（prompt 是助手的回答），截到 500 字
	ChatID    string    `json:"chat_id,omitempty"` // prompt：这次开的对话
}

type scheduleState struct {
	Disabled bool         `json:"disabled,omitempty"`
	Last     *scheduleRun `json:"last,omitempty"`
	LastOK   *time.Time   `json:"last_ok,omitempty"`
	Fails    int          `json:"fails,omitempty"` // 连续失败次数
}

// scheduler 管本机所有项目的定时任务：每个项目各自的定义（项目的 schedules/）、运行记录和暂停状态
// （~/.shuttle/workspaces/<项目>/schedules.json）。当前打开的项目什么都跑；别的项目只跑本机函数（fn），
// 交给助手的（task / prompt）要等打开那个项目时才跑：助手的工作目录、对话跟着当前项目。
type scheduler struct {
	mu       sync.Mutex
	projects map[string]*projSched
}

type projSched struct {
	dir     string
	defs    []scheduleDef
	defsErr error
	sig     string // schedules/ 的指纹（dirSignature）
	state   map[string]*scheduleState
	file    string
	running map[string]*scheduleRun // 正在跑的这次（交给助手的带着这次开的对话）
}

// proj 取一个项目的调度状态（第一次读运行记录），schedules/ 变了就重读定义。调用方持有 sc.mu。
func (s *Server) proj(pid, dir string) *projSched {
	sc := &s.sched
	if sc.projects == nil {
		sc.projects = map[string]*projSched{}
	}
	p := sc.projects[pid]
	if p == nil {
		p = &projSched{file: filepath.Join(s.cfg.Dir, "workspaces", pid, "schedules.json"), state: map[string]*scheduleState{}, running: map[string]*scheduleRun{}}
		if b, err := os.ReadFile(p.file); err == nil {
			json.Unmarshal(b, &p.state)
		}
		sc.projects[pid] = p
	}
	sig := dirSignature(dir, WorkspaceSchedulesDir)
	if p.dir != dir || p.sig != sig || sig == "" {
		p.dir, p.sig = dir, sig
		p.defs, p.defsErr = parseSchedules(dir)
		if p.defsErr != nil {
			log.Print(p.defsErr)
		}
	}
	return p
}

func (p *projSched) save() {
	os.MkdirAll(filepath.Dir(p.file), 0o700)
	b, _ := json.MarshalIndent(p.state, "", "  ")
	os.WriteFile(p.file, b, 0o600)
}

// schedProject 是一个有定时任务可跑的本机项目。
type schedProject struct {
	ID, Dir, Name    string
	Offline, Current bool
}

// scheduleProjects：当前项目在最前；再加本机别的项目——离线的都算，在线的要和当前项目在同一个 creght 区域、这个区域登录着（要读写它在 creght 上的表）。
func (s *Server) scheduleProjects() []schedProject {
	if !s.ready.Load() {
		return nil
	}
	cur := schedProject{ID: s.ws.ProjectID, Dir: s.ws.Dir, Offline: s.ws.Offline, Current: true}
	cur.Name = s.projectName(cur.ID, cur.Dir)
	out := []schedProject{cur}
	signedIn := s.signedIn()
	dirs := []string{}
	ents, _ := os.ReadDir(filepath.Join(s.cfg.Dir, "backends"))
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(s.cfg.Dir, "backends", e.Name()))
		}
	}
	if s.cfg.LegacyProject() != "" {
		dirs = append(dirs, filepath.Join(s.cfg.Dir, "backend"))
	}
	seen := map[string]bool{cur.ID: true}
	for _, d := range dirs {
		ws, err := creght.OpenWorkspaceOn(d, s.ws.APIHost)
		if err != nil || seen[ws.ProjectID] {
			continue
		}
		if !ws.Offline && (ws.APIHost != s.ws.APIHost || !signedIn) {
			continue
		}
		seen[ws.ProjectID] = true
		out = append(out, schedProject{ID: ws.ProjectID, Dir: ws.Dir, Name: s.projectName(ws.ProjectID, ws.Dir), Offline: ws.Offline})
	}
	return out
}

// projectName：离线项目读 .shuttle/project.json；在线项目用账号下项目列表里的名字（缓存的），查不到就是 id。
// 缓存也存一份在 <数据目录>/project-names.json：没连 creght、拉不到列表时还能显示上次见过的名字。
func (s *Server) projectName(pid, dir string) string {
	if p, err := creght.ReadOffline(dir); err == nil {
		return p.Name
	}
	file := filepath.Join(s.cfg.Dir, "project-names.json")
	projectNames.Lock()
	defer projectNames.Unlock()
	if !projectNames.read {
		projectNames.read = true
		if b, err := os.ReadFile(file); err == nil {
			json.Unmarshal(b, &projectNames.m)
		}
	}
	if time.Since(projectNames.at) > 5*time.Minute && !projectNames.loading {
		projectNames.loading = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := s.listBackends(ctx)
			projectNames.Lock()
			defer projectNames.Unlock()
			projectNames.loading, projectNames.at = false, time.Now()
			if err == nil {
				for _, b := range list {
					projectNames.m[b.ProjectID] = b.Name
				}
				if b, err := json.Marshal(projectNames.m); err == nil {
					os.WriteFile(file, b, 0o644)
				}
			}
		}()
	}
	if n := projectNames.m[pid]; n != "" {
		return n
	}
	return pid
}

var projectNames = struct {
	sync.Mutex
	m       map[string]string
	at      time.Time
	loading bool
	read    bool // 读过 project-names.json 了
}{m: map[string]string{}}

// parseSchedules 读 root/schedules/*.json，每个文件一条，文件名是 id。
func parseSchedules(root string) ([]scheduleDef, error) {
	files, err := readJSONDir(root, WorkspaceSchedulesDir)
	if err != nil {
		return nil, err
	}
	if files == nil {
		if _, err := os.Stat(filepath.Join(root, legacySchedulesFile)); err == nil {
			return nil, i18n.Errorf("定时任务还是旧格式（%s），这版 Annulo 不再读它：到 设置 → 项目 升级模板（新格式是 %s/<id>.json，一个任务一个文件）",
				"Scheduled tasks are still in the old format (%s), which this Annulo no longer reads: upgrade the template in Settings → Project (the new format is %s/<id>.json, one file per task)", legacySchedulesFile, WorkspaceSchedulesDir)
		}
		return nil, nil
	}
	defs := make([]scheduleDef, 0, len(files))
	for _, f := range files {
		var d scheduleDef
		if err := json.Unmarshal(f.Data, &d); err != nil {
			return nil, i18n.Errorf("%s 格式不对：%w", "%s is malformed: %w", f.Rel, err)
		}
		d.ID = plugin.Name(f.Plugin, f.Key) // 插件的带插件 id：plugins/social/schedules/x.collect.json → social/x.collect
		d.Prompt = strings.TrimSpace(d.Prompt)
		switch {
		case btoi(d.Fn != "")+btoi(d.Prompt != "")+btoi(d.Task != "") > 1:
			return nil, i18n.Errorf("%s：fn、prompt、task 只能写一个", "%s: use only one of fn, prompt and task", f.Rel)
		case d.Task != "":
			if _, err := tasks.Load(root, d.Task); err != nil {
				return nil, i18n.Errorf("%s：%w", "%s: %w", f.Rel, err)
			}
			if !chatKeyRe.MatchString(f.Key) {
				return nil, i18n.Errorf("%s：task 的文件名只能用字母、数字、- 和 _", "%s: a task's file name may only use letters, digits, - and _", f.Rel)
			}
		case d.Prompt != "":
			if !chatKeyRe.MatchString(f.Key) {
				return nil, i18n.Errorf("%s：prompt 的文件名只能用字母、数字、- 和 _（比如 weekly-report.json）", "%s: a prompt's file name may only use letters, digits, - and _ (e.g. weekly-report.json)", f.Rel)
			}
		default:
			if _, fn, ok := strings.Cut(d.Fn, "."); !ok || fn == "" {
				return nil, i18n.Errorf("%s：fn 要写成 文件.函数（比如 leads.sync），或者写 prompt 交给助手", "%s: fn must be file.function (e.g. leads.sync), or use prompt to hand it to the assistant", f.Rel)
			}
		}
		switch {
		case d.At != "":
			if !atRe.MatchString(d.At) {
				return nil, i18n.Errorf("%s：at 要写成 HH:MM（比如 09:00）", "%s: at must be HH:MM (e.g. 09:00)", f.Rel)
			}
		case d.Every != "":
			v, err := parseEvery(d.Every)
			if err != nil {
				return nil, i18n.Errorf("%s：every 不对：%w", "%s: every is invalid: %w", f.Rel, err)
			}
			d.every = v
		default:
			return nil, i18n.Errorf("%s：要写 every（比如 \"30m\"、\"6h\"、\"1d\"）或 at（比如 \"09:00\"）", "%s: needs every (e.g. \"30m\", \"6h\", \"1d\") or at (e.g. \"09:00\")", f.Rel)
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// parseEvery 认 Go 的时长（30m、6h）和天（1d、7d）。
func parseEvery(s string) (time.Duration, error) {
	var v time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days int
		if _, err := fmt.Sscanf(n, "%d", &days); err != nil || days <= 0 {
			return 0, i18n.Errorf("%q 不是时长", "%q isn't a duration", s)
		}
		v = time.Duration(days) * 24 * time.Hour
	} else {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, i18n.Errorf("%q 不是时长（写成 30m、6h、1d）", "%q isn't a duration (write 30m, 6h, 1d)", s)
		}
		v = d
	}
	if v < minEvery {
		return 0, i18n.Errorf("最短 %s 一次", "At most once every %s", minEvery)
	}
	return v, nil
}

// runSchedules 每半分钟看一眼有没有到点的任务。同一个任务上一次没跑完就不重复开。
func (s *Server) runSchedules(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	// 刚启动时等一会儿：MCP 在连、业务表在补
	select {
	case <-time.After(20 * time.Second):
	case <-ctx.Done():
		return
	}
	for {
		s.tickSchedules()
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) tickSchedules() {
	if !s.ready.Load() {
		return
	}
	now := time.Now()
	type job struct {
		p schedProject
		d scheduleDef
	}
	var due []job
	sc := &s.sched
	for _, sp := range s.scheduleProjects() {
		sc.mu.Lock()
		p := s.proj(sp.ID, sp.Dir)
		for _, d := range p.defs {
			if !sp.Current && d.Fn == "" {
				continue // 交给助手的只在打开这个项目时跑
			}
			st := p.state[d.key()]
			if st != nil && st.Disabled || p.running[d.key()] != nil {
				continue
			}
			var last time.Time
			if st != nil && st.Last != nil {
				last = st.Last.StartedAt
			}
			if !d.next(last, now).After(now) {
				due = append(due, job{sp, d})
			}
		}
		sc.mu.Unlock()
	}
	for _, j := range due {
		s.startScheduled(j.p, j.d)
	}
}

// startScheduled 在后台跑一次，结果记进那个项目的运行记录。不是当前项目的只跑本机函数（在那个项目的目录、表里跑）。
func (s *Server) startScheduled(sp schedProject, d scheduleDef) bool {
	sc := &s.sched
	key := d.key()
	sc.mu.Lock()
	p := s.proj(sp.ID, sp.Dir)
	if p.running[key] != nil {
		sc.mu.Unlock()
		return false
	}
	start := time.Now()
	current := s.ready.Load() && s.ws.ProjectID == sp.ID
	run := &scheduleRun{StartedAt: start}
	if current && (d.Task != "" || d.Prompt != "") {
		// 对话 id 先定下来，跑的过程中界面的「查看对话」就能打开这一次的
		run.ChatID = fmt.Sprintf("sched-%s-%d", plugin.ChatKey(key), start.Unix())
	}
	p.running[key] = run
	sc.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), localRunTimeout)
		defer cancel()
		var err error
		switch {
		case (d.Task != "" || d.Prompt != "") && !current:
			err = i18n.New("交给助手的定时任务只在打开这个项目时跑", "Scheduled assistant tasks only run while this project is open")
		case d.Task != "":
			var answer string
			answer, err = s.execTask(d.Task, d.Input, run.ChatID, d.Name, true, start)
			run.Result = truncateRunes(answer, 500)
		case d.Prompt != "":
			var answer string
			answer, err = s.runScheduledPrompt(d, run.ChatID, start)
			run.Result = truncateRunes(answer, 500)
		default:
			var host localfn.Host
			if host, err = s.projectHost(sp.ID); err == nil {
				host.RunID = fmt.Sprintf("s%d", start.UnixNano())
				var res any
				res, err = localfn.Run(ctx, host, d.Fn, d.Input, func(localfn.Event) {})
				if err == nil {
					b, _ := json.Marshal(res)
					run.Result = truncateRunes(string(b), 500)
				}
			}
		}
		run.Ms, run.OK = time.Since(start).Milliseconds(), err == nil
		if err != nil {
			run.Error = err.Error()
			log.Printf("定时任务 %s/%s 失败（%dms）：%v", sp.ID, key, run.Ms, err)
		} else {
			log.Printf("定时任务 %s/%s 完成（%dms）", sp.ID, key, run.Ms)
		}
		sc.mu.Lock()
		defer sc.mu.Unlock()
		delete(p.running, key)
		st := p.state[key]
		if st == nil {
			st = &scheduleState{}
			p.state[key] = st
		}
		st.Last = run
		if run.OK {
			st.LastOK, st.Fails = &start, 0
		} else {
			st.Fails++
		}
		p.save()
	}()
	return true
}

// runScheduledPrompt 开一段新对话，把定时任务的提示交给助手跑完一轮（和用户在界面上发消息走同一条路：
// 边跑边存、界面能续看、结束自动提交项目）。返回助手最后的回答。
func (s *Server) runScheduledPrompt(d scheduleDef, chatID string, start time.Time) (string, error) {
	name := d.Name
	if name == "" {
		name = d.ID
	}
	prompt := fmt.Sprintf(scheduledNote(), name) + d.Prompt
	return s.runBackgroundChat(chatID, i18n.T("定时：", "Scheduled: ")+name, prompt, start)
}

// runBackgroundChat 开一段新对话，把 prompt 交给助手跑完一轮（和用户在界面上发消息走同一条路：
// 边跑边存、界面能续看、结束自动提交项目）。返回助手最后的回答。定时的提示和项目的任务都走它。
func (s *Server) runBackgroundChat(chatID, title, prompt string, start time.Time) (string, error) {
	exec, err := s.beginBackgroundChat(chatID, title, prompt, start)
	if err != nil {
		return "", err
	}
	return exec()
}

// beginBackgroundChat 先把对话存下来、登记成正在跑，返回真正跑这一轮的函数（调用方放到后台）。
// 页面拿到 chat_id 马上就去打开这段对话：要在返回 chat_id 之前做完这一步，不然会读到「对话不存在」。
func (s *Server) beginBackgroundChat(chatID, title, prompt string, start time.Time) (func() (string, error), error) {
	startedAt := start.UnixMilli()
	user, _ := json.Marshal(uiMessage{ID: "u" + chatID, Role: "user", Parts: []uiPart{{Type: "text", Text: prompt}}})
	if err := s.agent.SaveMessage(chatID, user, title, startedAt); err != nil {
		return nil, err
	}
	// 标题直接用任务名，不再让模型起名
	if err := s.agent.SetTitle(chatID, title, "auto"); err != nil {
		log.Printf("给对话 %s 起名失败：%v", chatID, err)
	}
	run := newRun()
	s.runsMu.Lock()
	s.runs[chatID] = run
	s.runsMu.Unlock()
	return func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), promptRunTimeout)
		defer cancel()
		answer, err := s.execRun(ctx, chatID, prompt, nil, startedAt, run, nil)
		if err != nil && ctx.Err() != nil {
			err = i18n.Errorf("超过 %s 没跑完，已停止", "Not finished after %s, so it was stopped", promptRunTimeout)
		}
		return answer, err
	}, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

type scheduleInfo struct {
	ID       string       `json:"id"`
	Fn       string       `json:"fn,omitempty"`
	Prompt   string       `json:"prompt,omitempty"` // 截到 200 字
	Task     string       `json:"task,omitempty"`
	Name     string       `json:"name"`
	Every    string       `json:"every,omitempty"`
	At       string       `json:"at,omitempty"`
	Disabled bool         `json:"disabled"`
	Running  bool         `json:"running"`
	ChatID   string       `json:"chat_id,omitempty"` // 正在跑的这次开的对话（交给助手的）
	NextAt   *time.Time   `json:"next_at,omitempty"`
	Last     *scheduleRun `json:"last,omitempty"`
	LastOK   *time.Time   `json:"last_ok,omitempty"`
	Fails    int          `json:"fails"`
	// OnlyWhenOpen：别的项目里交给助手的任务（task / prompt），要打开那个项目才跑
	OnlyWhenOpen bool `json:"only_when_open,omitempty"`
}

// scheduleList 是一个项目的定时任务。
func (s *Server) scheduleList(sp schedProject) ([]scheduleInfo, error) {
	sc := &s.sched
	sc.mu.Lock()
	defer sc.mu.Unlock()
	p := s.proj(sp.ID, sp.Dir)
	now := time.Now()
	out := []scheduleInfo{}
	for _, d := range p.defs {
		in := scheduleInfo{ID: d.key(), Fn: d.Fn, Prompt: truncateRunes(d.Prompt, 200), Task: d.Task, Name: d.Name, Every: d.Every, At: d.At, Running: p.running[d.key()] != nil}
		if r := p.running[d.key()]; r != nil {
			in.ChatID = r.ChatID
		}
		var last time.Time
		if st := p.state[d.key()]; st != nil {
			in.Disabled, in.Last, in.LastOK, in.Fails = st.Disabled, st.Last, st.LastOK, st.Fails
			if st.Last != nil {
				last = st.Last.StartedAt
			}
		}
		in.OnlyWhenOpen = !sp.Current && d.Fn == ""
		if !in.Disabled && !in.OnlyWhenOpen {
			n := d.next(last, now)
			if n.Before(now) {
				n = now
			}
			in.NextAt = &n
		}
		out = append(out, in)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, p.defsErr
}

type scheduleGroup struct {
	ProjectID string         `json:"project_id"`
	Name      string         `json:"name"`
	Offline   bool           `json:"offline,omitempty"`
	Current   bool           `json:"current,omitempty"`
	List      []scheduleInfo `json:"list"`
	Error     string         `json:"error,omitempty"`
}

// scheduleResponse：{ list, error（当前项目的，老界面用）, file, projects: [每个项目一组，当前的在前] }
func (s *Server) scheduleResponse() map[string]any {
	groups := []scheduleGroup{}
	res := map[string]any{"list": []scheduleInfo{}, "error": "", "file": WorkspaceSchedulesDir + "/"}
	for _, sp := range s.scheduleProjects() {
		list, err := s.scheduleList(sp)
		if sp.Current {
			res["list"], res["error"] = list, errString(err)
		}
		if len(list) == 0 && err == nil && !sp.Current {
			continue
		}
		groups = append(groups, scheduleGroup{ProjectID: sp.ID, Name: sp.Name, Offline: sp.Offline, Current: sp.Current, List: list, Error: errString(err)})
	}
	res["projects"] = groups
	return res
}

// GET  local/schedules                            所有项目的定时任务和上次运行的结果（按项目分组）
// POST local/schedules/<id>/toggle?project=<项目>  { disabled } 暂停 / 恢复（不给 project 是当前项目）
// POST local/schedules/<id>/run?project=<项目>     立刻跑一次（后台跑，结果在列表里看）
func (s *Server) apiSchedules(w http.ResponseWriter, r *http.Request, rest string) {
	if rest == "" && r.Method == http.MethodGet {
		writeJSON(w, s.scheduleResponse())
		return
	}
	pid := r.URL.Query().Get("project")
	var sp *schedProject
	for _, x := range s.scheduleProjects() {
		if x.ID == pid || (pid == "" && x.Current) {
			x := x
			sp = &x
			break
		}
	}
	if sp == nil {
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个项目：%s", "No such project: %s", pid))
		return
	}
	id, action := splitNameAction(sp.Dir, rest)
	sc := &s.sched
	sc.mu.Lock()
	p := s.proj(sp.ID, sp.Dir)
	var def *scheduleDef
	for i := range p.defs {
		if p.defs[i].key() == id {
			def = &p.defs[i]
		}
	}
	if def == nil || r.Method != http.MethodPost {
		sc.mu.Unlock()
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个定时任务：%s", "No such scheduled task: %s", id))
		return
	}
	d := *def
	switch action {
	case "toggle":
		var in struct {
			Disabled bool `json:"disabled"`
		}
		if err := readJSON(r, &in); err != nil {
			sc.mu.Unlock()
			fail(w, http.StatusBadRequest, err)
			return
		}
		st := p.state[id]
		if st == nil {
			st = &scheduleState{}
			p.state[id] = st
		}
		st.Disabled = in.Disabled
		p.save()
		sc.mu.Unlock()
	case "run":
		sc.mu.Unlock()
		if !sp.Current && d.Fn == "" {
			fail(w, http.StatusConflict, i18n.New("交给助手的定时任务只在打开这个项目时跑：先切到这个项目", "Scheduled assistant tasks only run while their project is open: switch to it first"))
			return
		}
		if !s.startScheduled(*sp, d) {
			fail(w, http.StatusConflict, i18n.New("这个任务正在跑", "This task is already running"))
			return
		}
	default:
		sc.mu.Unlock()
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个操作：%s", "No such action: %s", action))
		return
	}
	writeJSON(w, s.scheduleResponse())
}
