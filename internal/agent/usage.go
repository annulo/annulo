package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"

	"github.com/annulo/annulo/internal/config"
)

// token 用量只存本机：每次模型请求（pi 每条助手消息对应一次请求）往 ~/.shuttle/usage.jsonl 追加一行，
// 统计时现场聚合。一年几万行，全读一遍也就几十毫秒，不值得建索引。

type UsageRecord struct {
	TS         int64   `json:"ts"` // 毫秒
	Chat       string  `json:"chat,omitempty"`
	Workspace  string  `json:"workspace,omitempty"` // 用量是全局统计，记下来自哪个项目
	Model      string  `json:"model"`
	Input      int     `json:"input"` // 未命中缓存的输入
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Cost       float64 `json:"cost,omitempty"`
}

func (r UsageRecord) total() int { return r.Input + r.Output + r.CacheRead + r.CacheWrite }

var usageMu sync.Mutex

func (a *Agent) usageFile() string { return filepath.Join(a.cfg.Dir, "usage.jsonl") }

func (a *Agent) recordUsage(chat string, am *ai.AssistantMessage) {
	u := am.Usage
	if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return
	}
	r := UsageRecord{TS: time.Now().UnixMilli(), Chat: chat, Workspace: a.ws, Model: am.Model, Input: u.Input, Output: u.Output,
		CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Cost: u.Cost.Total}
	b, _ := json.Marshal(r)
	usageMu.Lock()
	defer usageMu.Unlock()
	f, err := os.OpenFile(a.usageFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

type UsageBucket struct {
	Requests   int     `json:"requests"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Total      int     `json:"total"`
	Cost       float64 `json:"cost"`
}

func (b *UsageBucket) add(r UsageRecord) {
	b.Requests++
	b.Input += r.Input
	b.Output += r.Output
	b.CacheRead += r.CacheRead
	b.CacheWrite += r.CacheWrite
	b.Total += r.total()
	b.Cost += r.Cost
}

type UsageDay struct {
	Day string `json:"day"` // 2026-09-25，本地时区
	UsageBucket
}

type UsageModel struct {
	Model string `json:"model"`
	UsageBucket
}

type UsageReport struct {
	Days    int          `json:"days"`
	Totals  UsageBucket  `json:"totals"` // 最近 days 天
	Today   UsageBucket  `json:"today"`
	Daily   []UsageDay   `json:"daily"`   // 最近 days 天，每天一格（没有用量的是 0）
	Heatmap []UsageDay   `json:"heatmap"` // 最近 53 周，从周一开始，热力图用
	Models  []UsageModel `json:"models"`  // 最近 days 天按模型，按总量降序
	Hours   [24]int      `json:"hours"`   // 最近 days 天按小时的 token 总量
}

// Usage 聚合最近 days 天的用量；热力图固定给最近 53 周。
func (a *Agent) Usage(days int) (*UsageReport, error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := today.AddDate(0, 0, -(days - 1))
	// 热力图从 52 周前的那个周一开始，到今天
	wd := (int(today.Weekday()) + 6) % 7 // 周一 = 0
	heatStart := today.AddDate(0, 0, -wd-52*7)

	byDay := map[string]*UsageBucket{}
	byModel := map[string]*UsageBucket{}
	rep := &UsageReport{Days: days}

	usageMu.Lock()
	f, err := os.Open(a.usageFile())
	usageMu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if f != nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var r UsageRecord
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			t := time.UnixMilli(r.TS)
			if t.Before(heatStart) {
				continue
			}
			day := t.Format("2006-01-02")
			if byDay[day] == nil {
				byDay[day] = &UsageBucket{}
			}
			byDay[day].add(r)
			if !t.Before(rangeStart) {
				rep.Totals.add(r)
				rep.Hours[t.Hour()] += r.total()
				if byModel[r.Model] == nil {
					byModel[r.Model] = &UsageBucket{}
				}
				byModel[r.Model].add(r)
			}
			if !t.Before(today) {
				rep.Today.add(r)
			}
		}
	}
	for d := rangeStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		day := UsageDay{Day: k}
		if b := byDay[k]; b != nil {
			day.UsageBucket = *b
		}
		rep.Daily = append(rep.Daily, day)
	}
	for d := heatStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		day := UsageDay{Day: k}
		if b := byDay[k]; b != nil {
			day.UsageBucket = *b
		}
		rep.Heatmap = append(rep.Heatmap, day)
	}
	for m, b := range byModel {
		rep.Models = append(rep.Models, UsageModel{Model: m, UsageBucket: *b})
	}
	sort.Slice(rep.Models, func(i, j int) bool { return rep.Models[i].Total > rep.Models[j].Total })
	return rep, nil
}

// ImportUsageOnce 在本机还没有用量文件时，从 pi 的会话文件里补出历史用量（每条助手消息带用量和时间）。
// 只跑一次：用量文件存在后就以它为准。
func (a *Agent) ImportUsageOnce() {
	if _, err := os.Stat(a.usageFile()); err == nil {
		return
	}
	dir := coding.DefaultSessionDir(a.cwd)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var recs []UsageRecord
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var line struct {
				Type    string `json:"type"`
				Message struct {
					Role      string   `json:"role"`
					Model     string   `json:"model"`
					Timestamp int64    `json:"timestamp"`
					Usage     ai.Usage `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "message" || line.Message.Role != "assistant" {
				continue
			}
			u := line.Message.Usage
			r := UsageRecord{TS: line.Message.Timestamp, Model: line.Message.Model, Input: u.Input, Output: u.Output,
				CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Cost: u.Cost.Total}
			if r.total() > 0 && r.TS > 0 {
				recs = append(recs, r)
			}
		}
		f.Close()
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].TS < recs[j].TS })
	var buf []byte
	for _, r := range recs {
		b, _ := json.Marshal(r)
		buf = append(append(buf, b...), '\n')
	}
	os.WriteFile(a.usageFile(), buf, 0o600)
}

// tokenMix 是本机最近的 token 构成（选「最便宜的模型」用），记录不到 20 次请求时用默认构成。
// 读 usage.jsonl 有成本，结果缓存 10 分钟。
func (a *Agent) tokenMix() config.TokenMix {
	mixCache.mu.Lock()
	defer mixCache.mu.Unlock()
	if !mixCache.at.IsZero() && time.Since(mixCache.at) < 10*time.Minute {
		return mixCache.mix
	}
	mixCache.at, mixCache.mix = time.Now(), config.DefaultTokenMix
	f, err := os.Open(a.usageFile())
	if err != nil {
		return mixCache.mix
	}
	defer f.Close()
	var in, cached, out float64
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r UsageRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		in += float64(r.Input + r.CacheWrite)
		cached += float64(r.CacheRead)
		out += float64(r.Output)
		n++
	}
	if total := in + cached + out; n >= 20 && total > 0 {
		mixCache.mix = config.TokenMix{Cached: cached / total, Input: in / total, Output: out / total}
	}
	return mixCache.mix
}

var mixCache struct {
	mu  sync.Mutex
	at  time.Time
	mix config.TokenMix
}
