package inbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store 是事件存在哪：本机和服务端现在都是文件（FileStore）；以后云端队列、Postgres 是别的实现。
type Store interface {
	// Put 存一条新事件（补上 id、状态）。同一个收件箱里 dedupe_key 一样的已经有了：dup 是 true，不存。
	Put(e *Event) (dup bool, err error)
	Get(inbox, id string) (Event, error)
	Update(e Event) error
	// List 最新的在前，最多 limit 条（0 是全部）。
	List(inbox string, limit int) ([]Event, error)
	// Pending 等处理的，先进来的在前。
	Pending(inbox string, limit int) ([]Event, error)
	// Prune 删处理完、超过 keep 的。
	Prune(inbox string, keep time.Duration) error
	// Cursor、SetCursor：拉来源读到哪了。
	Cursor(inbox, source string) ([]byte, error)
	SetCursor(inbox, source string, cursor []byte) error
}

// FileStore：<dir>/<收件箱>/events/<事件 id>.json，游标在 <dir>/<收件箱>/cursors/<来源>。
// 事件 id 由收件箱和 dedupe_key 算出来，去重就是看文件在不在。
type FileStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (s *FileStore) inboxDir(inbox string) string {
	return filepath.Join(s.dir, url.PathEscape(inbox)) // 插件的收件箱 id 带 /
}

// EventID 由收件箱和 dedupe_key 算出来。
func EventID(inbox, dedupe string) string {
	h := sha256.Sum256([]byte(inbox + "\x00" + dedupe))
	return "e" + hex.EncodeToString(h[:10])
}

var ErrNotFound = errors.New("event not found")

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\.`)
}

func (s *FileStore) path(inbox, id string) string {
	return filepath.Join(s.inboxDir(inbox), "events", id+".json")
}

func writeFile(path string, b []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

func (s *FileStore) Put(e *Event) (bool, error) {
	e.ID = EventID(e.Inbox, e.DedupeKey)
	if e.Status == "" {
		e.Status = Pending
	}
	if e.ReceivedAt.IsZero() {
		e.ReceivedAt = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	p := s.path(e.Inbox, e.ID)
	tmp, err := writeFile(p, b)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	// Link 在目标已经存在时失败：两个同样的请求同时进来也只存一份
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (s *FileStore) Get(inbox, id string) (Event, error) {
	var e Event
	if !validID(id) {
		return e, ErrNotFound
	}
	b, err := os.ReadFile(s.path(inbox, id))
	if os.IsNotExist(err) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(b, &e)
}

func (s *FileStore) Update(e Event) error {
	if !validID(e.ID) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(e.Inbox, e.ID)
	if _, err := os.Stat(p); err != nil {
		return ErrNotFound
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := writeFile(p, b)
	if err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *FileStore) all(inbox string) ([]Event, error) {
	dir := filepath.Join(s.inboxDir(inbox), "events")
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(ents))
	for _, en := range ents {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".json") || strings.HasPrefix(en.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, en.Name()))
		if err != nil {
			continue
		}
		var e Event
		if json.Unmarshal(b, &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *FileStore) List(inbox string, limit int) ([]Event, error) {
	all, err := s.all(inbox)
	sort.Slice(all, func(i, j int) bool { return all[i].ReceivedAt.After(all[j].ReceivedAt) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, err
}

func (s *FileStore) Pending(inbox string, limit int) ([]Event, error) {
	all, err := s.all(inbox)
	var out []Event
	for _, e := range all {
		if e.Status == Pending {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.Before(out[j].ReceivedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, err
}

func (s *FileStore) Prune(inbox string, keep time.Duration) error {
	all, err := s.all(inbox)
	if err != nil {
		return err
	}
	cut := time.Now().Add(-keep)
	for _, e := range all {
		if e.Status != Pending && e.ReceivedAt.Before(cut) {
			os.Remove(s.path(inbox, e.ID))
		}
	}
	return nil
}

func (s *FileStore) cursorPath(inbox, source string) string {
	return filepath.Join(s.inboxDir(inbox), "cursors", url.PathEscape(source))
}

func (s *FileStore) Cursor(inbox, source string) ([]byte, error) {
	b, err := os.ReadFile(s.cursorPath(inbox, source))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func (s *FileStore) SetCursor(inbox, source string, cursor []byte) error {
	p := s.cursorPath(inbox, source)
	tmp, err := writeFile(p, cursor)
	if err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
