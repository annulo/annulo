package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// 运营后台的业务表由运营后台自己声明：项目根目录的 tables/ 目录，一张表一个文件 tables/<key>.json（{ name, desc, json_schema }），文件名就是表的 key。
// Shuttle 不认识任何业务表，只按这些文件建表、放行数据接口和本机函数的 ctx.db。
// 复制项目只会带走站点代码、不带表，所以每次启动（和文件变了时）检查一遍，缺哪张建哪张。
// agent 给后台加模块时加一个文件，不改 Shuttle。

// legacy_tables.json 只用于迁移：最早的运营后台什么表声明都没有，表结构写在 Shuttle 里。
// 遇到这样的工作区，按它写出 tables/ 一次，之后就只看工作区里的文件。
//
//go:embed legacy_tables.json
var legacyTablesJSON []byte

// WorkspaceTablesDir 是运营后台自己声明的表（一张表一个 <key>.json）。
const WorkspaceTablesDir = "tables"

// legacyTablesFile 是老格式（一个大数组）：不再读，只用来提示项目要升级模板。
const legacyTablesFile = "shuttle.tables.json"

var tableKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

// workspaceTables 缓存 tables/ 的声明（按目录指纹），变了就在后台补建表。
type workspaceTables struct {
	mu   sync.Mutex
	root string
	sig  string
	defs []tableDef
	err  error
}

// migrateTablesFile：最早的运营后台什么表声明都没有（tables/ 和老的 shuttle.tables.json 都没有），按早期内置的表结构写出 tables/。
// 有老的 shuttle.tables.json 的不动：那是模板还没升级，升级模板会带来 tables/。
func migrateTablesFile(dir string) {
	if _, err := os.Stat(filepath.Join(dir, WorkspaceTablesDir)); err == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, legacyTablesFile)); err == nil {
		return
	}
	var defs []struct {
		Key string `json:"key"`
		tableDef
	}
	if err := json.Unmarshal(legacyTablesJSON, &defs); err != nil {
		log.Printf("读早期的表结构失败：%v", err)
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, WorkspaceTablesDir), 0o755); err != nil {
		log.Printf("建 %s 失败：%v", WorkspaceTablesDir, err)
		return
	}
	for _, d := range defs {
		b, _ := json.MarshalIndent(d.tableDef, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, WorkspaceTablesDir, d.Key+".json"), append(b, '\n'), 0o644); err != nil {
			log.Printf("写 %s/%s.json 失败：%v", WorkspaceTablesDir, d.Key, err)
			return
		}
	}
	log.Printf("运营后台没有表声明，按早期的表结构写了 %s/", WorkspaceTablesDir)
}

// tableDefs 是运营后台声明的全部业务表。
func (s *Server) tableDefs() ([]tableDef, error) {
	if !s.ready.Load() {
		return nil, nil
	}
	defs, changed, err := s.wsTables.load(s.ws.Dir)
	if changed && err == nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.EnsureTables(ctx); err != nil {
				log.Printf("补齐业务表失败：%v", err)
			}
		}()
	}
	return defs, err
}

// load 读 root/tables/*.json；目录没变用缓存。changed：这次读到的和上次不一样（包括第一次读到）。
func (t *workspaceTables) load(root string) (defs []tableDef, changed bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sig := dirSignature(root, WorkspaceTablesDir)
	if t.root == root && t.sig == sig && sig != "" {
		return t.defs, false, t.err
	}
	t.root, t.sig = root, sig
	t.defs, t.err = parseTables(root)
	if t.err != nil {
		log.Print(t.err)
	}
	return t.defs, t.err == nil && t.defs != nil, t.err
}

func parseTables(root string) ([]tableDef, error) {
	files, err := readJSONDir(root, WorkspaceTablesDir)
	if err != nil {
		return nil, err
	}
	if files == nil {
		if _, err := os.Stat(filepath.Join(root, legacyTablesFile)); err == nil {
			return nil, i18n.Errorf("业务表还是旧格式（%s），这版 Annulo 不再读它：到 设置 → 项目 升级模板（新格式是 %s/<表>.json，一张表一个文件）",
				"Data tables are still in the old format (%s), which this Annulo no longer reads: upgrade the template in Settings → Project (the new format is %s/<table>.json, one file per table)", legacyTablesFile, WorkspaceTablesDir)
		}
		return nil, nil
	}
	defs := make([]tableDef, 0, len(files))
	for _, f := range files {
		var d tableDef
		if err := json.Unmarshal(f.Data, &d); err != nil {
			return nil, i18n.Errorf("%s 格式不对：%w", "%s is malformed: %w", f.Rel, err)
		}
		d.Key = f.Key
		if !tableKeyRe.MatchString(d.Key) || len(d.JSONSchema) == 0 {
			return nil, i18n.Errorf("%s：文件名（表的 key）要是小写字母、数字、下划线，内容要有 json_schema", "%s: the file name (table key) must be lowercase letters, digits and underscores, and it needs a json_schema", f.Rel)
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// tableErr：表不在声明里时报什么。声明本身读不出来（比如还是旧格式、要升级模板）就报那个原因，别让人以为是表写错了。
func (s *Server) tableErr(fallback error) error {
	if _, err := s.tableDefs(); err != nil {
		return err
	}
	return fallback
}

// isOpsTable：数据接口、本机函数只放行业务表。
func (s *Server) isOpsTable(key string) bool {
	defs, _ := s.tableDefs()
	for _, d := range defs {
		if d.Key == key {
			return true
		}
	}
	return false
}

type tableDef struct {
	Key        string          `json:"-"` // 文件名（tables/<key>.json）
	Name       string          `json:"name"`
	Desc       string          `json:"desc"`
	JSONSchema json.RawMessage `json:"json_schema"`
}

// EnsureTables 在运营后台项目里建出缺少的业务表。
func (s *Server) EnsureTables(ctx context.Context) error {
	if !s.ready.Load() || s.ws.Offline {
		return nil // 离线项目的表在本机，不用建
	}
	defs, err := s.tableDefs()
	if err != nil {
		return err
	}
	have, err := s.creght.TableKeys(ctx, s.ws.ProjectID)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if have[d.Key] {
			continue
		}
		if err := s.creght.CreateTable(ctx, s.ws.ProjectID, d.Key, d.Name, d.Desc, d.JSONSchema); err != nil {
			return i18n.Errorf("建表 %s 失败：%w", "Failed to create table %s: %w", d.Key, err)
		}
		log.Printf("建表 %s（%s）", d.Key, d.Name)
	}
	return nil
}
