package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/wsgit"
)

// 内置的模板仓库、插件仓库里有哪些模板 / 插件：实时读仓库最新一版（semver tag）里的一级子目录，
// 仓库加了新模板、打了 tag 就能选，不用等 Annulo 发版。
//
//   - 读过的存在 <数据目录>/catalog-<种类>.json，10 分钟内直接用；过期了先用旧的、后台再读一次；
//   - 从没读过（第一次打开）最多等 3 秒，网慢就先用随安装包带的那份（fallback），后台读完下次就是新的；
//   - 读失败（没网、GitHub 打不开）照用上次读到的，1 分钟后再试。

type catalog struct {
	kind     string   // templates / plugins：缓存文件名、镜像目录
	repo     string   // 仓库地址
	markers  []string // 子目录里有这个文件才算
	fallback []string // 从没读到过时用的

	mu      sync.Mutex
	loaded  bool // 读过磁盘上的缓存
	dirs    []string
	at      time.Time // dirs 是什么时候读到的
	tried   time.Time // 上次去仓库读（成不成都算）
	running chan struct{}
}

var (
	// templateCatalog / pluginCatalog：测试里置 nil，不连 GitHub
	templateCatalog = &catalog{kind: "templates", repo: "https://github.com/annulo/templates", markers: []string{"annulo.json", "shuttle.json"}, fallback: []string{"blank", "creator"}}
	pluginCatalog   = &catalog{kind: "plugins", repo: "https://github.com/annulo/plugins", markers: []string{"plugin.json"}, fallback: []string{"social"}}
)

const (
	catalogFresh = 10 * time.Minute
	catalogRetry = time.Minute
	catalogWait  = 3 * time.Second
)

// sources 是这个仓库里的全部模板 / 插件，写成 <仓库>#<子目录>。
func (c *catalog) sources(dataDir string) []string {
	if c == nil || dataDir == "" {
		return nil // 没有数据目录（测试里）：不连 GitHub，也不往当前目录写镜像
	}
	var out []string
	for _, d := range c.list(dataDir) {
		out = append(out, c.repo+"#"+d)
	}
	return out
}

func (c *catalog) list(dataDir string) []string {
	c.mu.Lock()
	if !c.loaded {
		c.loaded = true
		c.readDisk(dataDir)
	}
	dirs, fresh := c.dirs, time.Since(c.at) < catalogFresh
	due := !fresh && time.Since(c.tried) > catalogRetry
	var wait chan struct{}
	if due {
		wait = c.refresh(dataDir)
	} else if c.running != nil && len(dirs) == 0 {
		wait = c.running
	}
	c.mu.Unlock()

	if len(dirs) > 0 {
		return dirs // 有就先用（过期的话后台已经在读了）
	}
	if wait != nil {
		select {
		case <-wait:
		case <-time.After(catalogWait):
		}
		c.mu.Lock()
		dirs = c.dirs
		c.mu.Unlock()
	}
	if len(dirs) == 0 {
		return c.fallback
	}
	return dirs
}

// refresh 在后台去仓库读一次（已经在读就不再起），返回读完时关闭的通道。调用方持有 c.mu。
func (c *catalog) refresh(dataDir string) chan struct{} {
	if c.running != nil {
		return c.running
	}
	done := make(chan struct{})
	c.running, c.tried = done, time.Now()
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dirs, err := wsgit.CatalogDirs(ctx, c.template(dataDir), c.markers...)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.running = nil
		if err != nil || len(dirs) == 0 {
			log.Printf("读 %s 里有哪些%s失败：%v", c.repo, map[string]string{"templates": "模板", "plugins": "插件"}[c.kind], err)
			return
		}
		if !slices.Equal(dirs, c.dirs) {
			log.Printf("%s 里的%s：%v", c.repo, map[string]string{"templates": "模板", "plugins": "插件"}[c.kind], dirs)
		}
		c.dirs, c.at = dirs, time.Now()
		c.writeDisk(dataDir)
	}()
	return done
}

func (c *catalog) template(dataDir string) wsgit.Template {
	site := wsgit.GitSite(c.repo, "")
	sum := sha1.Sum([]byte(site))
	return wsgit.Template{Site: site, Cache: filepath.Join(dataDir, c.kind, "catalog-"+hex.EncodeToString(sum[:6]))}
}

type catalogFile struct {
	Repo string    `json:"repo"`
	Dirs []string  `json:"dirs"`
	At   time.Time `json:"at"`
}

func (c *catalog) file(dataDir string) string {
	return filepath.Join(dataDir, "catalog-"+c.kind+".json")
}

func (c *catalog) readDisk(dataDir string) {
	b, err := os.ReadFile(c.file(dataDir))
	if err != nil {
		return
	}
	var f catalogFile
	if json.Unmarshal(b, &f) == nil && f.Repo == c.repo && len(f.Dirs) > 0 {
		c.dirs, c.at = f.Dirs, f.At
	}
}

func (c *catalog) writeDisk(dataDir string) {
	b, _ := json.MarshalIndent(catalogFile{Repo: c.repo, Dirs: c.dirs, At: c.at}, "", "  ")
	os.WriteFile(c.file(dataDir), b, 0o600)
}
