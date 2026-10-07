// Package tasks 读项目里的任务：tasks/<id>.md，一个文件一件交给助手做的事（写周报、写文章…）。
//
//	---
//	name: 写运营周报
//	description: 一句话说明这个任务做什么（助手和页面都看它）
//	thinking: low   # 可选：这个任务固定用的思考档位，不跟设置走（抽资料这类不用深想的写 low）
//	---
//	给助手的完整说明：数据从哪来、怎么写、结果存到哪…
//
// 页面上的按钮和定时任务（schedules/ 里的 task）都按这份说明开一段对话交给助手，过程在对话里看得见。任务是什么由项目定，Shuttle 不认识具体业务。
//
// 任务说明是系统流程（取数、存表、格式），用户不用管；「怎么写」单独放：模板给默认的 prompts/<id>.md（可以没有），
// 用户在后台页面上改了就存到 user/prompts/<id>.md（比如加一句「写完发到我邮箱」），有它就用它，下一次就照新的做。
// user/ 是用户自己的东西，模板不写这个目录，所以模板升级不会和用户改的冲突。
package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"
)

// Dir 是项目里放任务的目录；PromptDir 放模板给的各任务「怎么写」（prompts/<任务 id>.md）；
// UserPromptDir 放用户改过的（user/prompts/<任务 id>.md），有就用它。
const (
	Dir           = "tasks"
	PromptDir     = "prompts"
	UserPromptDir = "user/prompts"
)

// IDRe：任务 id 会拼进对话 id，只收对话 id 认的字符（插件的任务 id 是 <插件>/<IDRe>，拼进对话 id 时 / 换掉，见 plugin.ChatKey）。
var IDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type Task struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Thinking：这个任务固定用的思考档位（frontmatter 的 thinking: low），空 = 跟设置走。抽资料、按格式填表这类不用深想的任务写 low，快很多
	Thinking string `json:"thinking,omitempty"`
	File        string `json:"file"` // 相对项目根目录，比如 tasks/weekly-report.md
	Path        string `json:"-"`    // 绝对路径
	Front       string `json:"-"`    // 原样的 frontmatter（含两行 ---），改正文时保留
	Body        string `json:"body"` // 说明正文
	// 怎么写：用户改过的（user/prompts/<id>.md）优先，否则是模板的默认（prompts/<id>.md）。PromptFile 是生效那份相对项目根目录的路径，
	// 两份都没有时 Prompt、PromptFile 都是空（这个任务没有给用户改的写法，比如配置发布这类系统任务）。PromptCustom：用的是用户改过的
	Prompt       string `json:"prompt"`
	PromptFile   string `json:"prompt_file,omitempty"`
	PromptCustom bool   `json:"prompt_custom,omitempty"`
	HasDefault   bool   `json:"prompt_has_default,omitempty"`
	root         string
}

// 插件的任务 id 带插件 id：social/write-x 是 plugins/social/tasks/write-x.md，默认写法 plugins/social/prompts/write-x.md，
// 用户改的 user/plugins/social/prompts/write-x.md（docs/plugins.md）。

// parts 拆出插件 id 和任务名；不对返回 ok=false。
func parts(id string) (pid, name string, ok bool) {
	pid, name, ok = plugin.Split(id)
	return pid, name, ok && IDRe.MatchString(name)
}

// rel 是任务相关文件相对项目根目录的路径：dir 是 Dir 或 PromptDir。
func rel(id, dir string) string {
	pid, name, _ := parts(id)
	return plugin.Rel(pid, dir) + "/" + name + ".md"
}

// UserPromptRel 是用户改过的写法相对项目根目录的路径（user/prompts/<id>.md、user/plugins/<插件>/prompts/<任务>.md）。
func UserPromptRel(id string) string { return userRel(id) }

func userRel(id string) string {
	pid, name, _ := parts(id)
	if pid == "" {
		return UserPromptDir + "/" + name + ".md"
	}
	return plugin.UserDir(pid) + "/" + PromptDir + "/" + name + ".md"
}

// Path 是任务文件的绝对路径。
func Path(root, id string) string { return filepath.Join(root, filepath.FromSlash(rel(id, Dir))) }

// PromptPath / UserPromptPath 是模板默认的、用户改过的「怎么写」的绝对路径（不一定存在）。
func PromptPath(root, id string) string {
	return filepath.Join(root, filepath.FromSlash(rel(id, PromptDir)))
}
func UserPromptPath(root, id string) string {
	return filepath.Join(root, filepath.FromSlash(userRel(id)))
}

// Load 读一个任务。
func Load(root, id string) (*Task, error) {
	if _, _, ok := parts(id); !ok {
		return nil, i18n.Errorf("任务 id 不对：%s（只能用字母、数字、- 和 _；插件的任务是 插件/任务，比如 social/write-x）", "Invalid task id: %s (letters, digits, - and _ only; a plugin's task is plugin/task, e.g. social/write-x)", id)
	}
	p := Path(root, id)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, i18n.Errorf("项目里没有这个任务：%s", "No such task in the project: %s", rel(id, Dir))
	}
	if err != nil {
		return nil, err
	}
	t := parse(string(b))
	t.ID, t.File, t.Path, t.root = id, rel(id, Dir), p, root
	if t.Name == "" {
		t.Name = id
	}
	t.loadPrompt()
	return t, nil
}

// List 列出项目和插件里的全部任务：项目的在前，各自按 id 排。目录不存在就是没有。
func List(root string) []*Task {
	var out []*Task
	for _, pid := range plugin.Sources(root) {
		ents, _ := os.ReadDir(filepath.Join(plugin.Root(root, pid), Dir))
		var list []*Task
		for _, e := range ents {
			name, ok := strings.CutSuffix(e.Name(), ".md")
			if e.IsDir() || !ok || !IDRe.MatchString(name) {
				continue
			}
			if t, err := Load(root, plugin.Name(pid, name)); err == nil {
				list = append(list, t)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		out = append(out, list...)
	}
	return out
}

// SaveBody 只换正文，frontmatter 原样保留。
func (t *Task) SaveBody(body string) error {
	body = strings.TrimSpace(body)
	content := body + "\n"
	if t.Front != "" {
		content = t.Front + "\n\n" + content
	}
	if err := os.WriteFile(t.Path, []byte(content), 0o644); err != nil {
		return err
	}
	t.Body = body
	return nil
}

func (t *Task) loadPrompt() {
	t.Prompt, t.PromptFile, t.PromptCustom, t.HasDefault = "", "", false, false
	read := func(p string) (string, bool) {
		b, err := os.ReadFile(p)
		return strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), err == nil
	}
	def, hasDef := read(PromptPath(t.root, t.ID))
	t.HasDefault = hasDef
	if user, ok := read(UserPromptPath(t.root, t.ID)); ok {
		t.Prompt, t.PromptFile, t.PromptCustom = user, userRel(t.ID), true
	} else if hasDef {
		t.Prompt, t.PromptFile = def, rel(t.ID, PromptDir)
	}
}

// SavePrompt 存用户改的「怎么写」（user/prompts/<id>.md），之后就用它，不再用模板的默认。
func (t *Task) SavePrompt(text string) error {
	p := UserPromptPath(t.root, t.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(strings.TrimSpace(text)+"\n"), 0o644); err != nil {
		return err
	}
	t.loadPrompt()
	return nil
}

// ResetPrompt 删掉用户改的，回到模板的默认。
func (t *Task) ResetPrompt() error {
	if err := os.Remove(UserPromptPath(t.root, t.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	t.loadPrompt()
	return nil
}

// parse 拆 frontmatter（只认 name、description、thinking 三个单行字段）和正文。
func parse(s string) *Task {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	t := &Task{}
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if i := strings.Index(rest, "\n---"); i >= 0 {
			head := rest[:i]
			after := rest[i+len("\n---"):]
			if j := strings.IndexByte(after, '\n'); j >= 0 {
				after = after[j+1:]
			} else {
				after = ""
			}
			t.Front = "---\n" + head + "\n---"
			for _, line := range strings.Split(head, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				switch strings.TrimSpace(k) {
				case "name":
					t.Name = v
				case "description":
					t.Description = v
				case "thinking":
					if slices.Contains(config.ThinkingLevels, v) {
						t.Thinking = v
					}
				}
			}
			s = after
		}
	}
	t.Body = strings.TrimSpace(s)
	return t
}
