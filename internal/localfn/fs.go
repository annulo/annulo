package localfn

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/dop251/goja"
)

// ctx.fs：读写项目的工作目录（Host.WorkFiles，本机是 ~/Annulo/<项目名>）。
// 助手做视频、写脚本的工程本来就放这里（系统提示「工作文件放哪」）；模板把一条内容的文件夹也放这里，
// 页面经本机函数列出、编辑里面的文件，用 /_annulo/work-files/<路径> 预览（internal/server/workfiles.go）。
// 路径都是相对工作目录、用 / 分隔的；不能是绝对路径、不能带 ..，软链接指到工作目录外面的不认。
//
//	ctx.fs.root                         工作目录的绝对路径（还没建也给）
//	ctx.fs.list(dir?)                   → [{ path, dir, size, mtime }]，往下所有层；跳过 . 开头的和 node_modules，最多 5000 条；目录不存在给 []
//	ctx.fs.stat(path)                   → { path, dir, size, mtime } 或 null
//	ctx.fs.read(path)                   → 文字（UTF-8，最多 10MB）
//	ctx.fs.write(path, text)            没有的目录自动建
//	ctx.fs.mkdir(path)
//	ctx.fs.move(from, to)               改名、挪位置；to 已经有了报错
//	ctx.fs.trash(path)                  挪进废纸篓（macOS 的废纸篓；别的系统是工作目录里的 .trash/）
//	ctx.fs.copy(from, to)               from 可以是项目的本机文件 'local:<name>'（页面上传的、资料库里的）；to 已经有了报错
//	ctx.fs.toLocal(path)                → 'local:<name>'：存成项目的本机文件（发布、放进表里用）；同一个文件没改过给同一个名字
//	ctx.fs.open(path, { reveal })       用系统程序打开 / 在访达里显示（只有本机版，ctx.fs.canOpen 是 true）
//
// mtime 是毫秒。

const (
	fsMaxRead = 10 << 20
	fsMaxList = 5000
)

func (r *runner) fsObject() map[string]any {
	root := r.h.WorkFiles
	need := func() {
		if root == "" {
			r.throw(i18n.New("这里不能用 ctx.fs", "ctx.fs isn't available here"))
		}
	}
	entry := func(rel string, st fs.FileInfo) map[string]any {
		size := st.Size()
		if st.IsDir() {
			size = 0
		}
		return map[string]any{"path": rel, "dir": st.IsDir(), "size": size, "mtime": st.ModTime().UnixMilli()}
	}
	return map[string]any{
		"root": root,
		// canOpen：这台机器能不能用系统程序打开（本机版能；服务端部署用户不在服务器前面，页面改成新标签页打开预览地址）
		"canOpen": r.h.Open != nil,
		"list": func(dir goja.Value) []map[string]any {
			need()
			rel := ""
			if dir != nil && !goja.IsUndefined(dir) && !goja.IsNull(dir) {
				rel = dir.String()
			}
			full, err := r.fsPath(rel, true)
			if errors.Is(err, fs.ErrNotExist) {
				return []map[string]any{}
			}
			if err != nil {
				r.throw(err)
			}
			out := []map[string]any{}
			filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
				if err != nil || p == full {
					return nil
				}
				if strings.HasPrefix(d.Name(), ".") || (d.IsDir() && d.Name() == "node_modules") {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if len(out) >= fsMaxList {
					return filepath.SkipAll
				}
				st, err := d.Info()
				if err != nil {
					return nil
				}
				sub, _ := filepath.Rel(full, p)
				out = append(out, entry(filepath.ToSlash(sub), st))
				return nil
			})
			return out
		},
		"stat": func(p string) any {
			need()
			full, err := r.fsPath(p, true)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				r.throw(err)
			}
			st, err := os.Stat(full)
			if err != nil {
				return nil
			}
			return entry(fsClean(p), st)
		},
		"read": func(p string) string {
			need()
			full := r.fsMust(p, true)
			st, err := os.Stat(full)
			if err != nil {
				r.throw(err)
			}
			if st.IsDir() {
				r.throw(i18n.Errorf("%s 是目录", "%s is a directory", p))
			}
			if st.Size() > fsMaxRead {
				r.throw(i18n.Errorf("%s 太大了（最多读 10MB）", "%s is too large (10MB max)", p))
			}
			b, err := os.ReadFile(full)
			if err != nil {
				r.throw(err)
			}
			return string(b)
		},
		"write": func(p, text string) {
			need()
			full := r.fsMust(p, false)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				r.throw(err)
			}
			if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
				r.throw(err)
			}
		},
		"mkdir": func(p string) {
			need()
			if err := os.MkdirAll(r.fsMust(p, false), 0o755); err != nil {
				r.throw(err)
			}
		},
		"move": func(from, to string) {
			need()
			src, dst := r.fsMust(from, true), r.fsMust(to, false)
			if src == root {
				r.throw(i18n.New("不能挪整个工作目录", "Can't move the whole work folder"))
			}
			if _, err := os.Lstat(dst); err == nil {
				r.throw(i18n.Errorf("%s 已经有了", "%s already exists", to))
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				r.throw(err)
			}
			if err := os.Rename(src, dst); err != nil {
				r.throw(err)
			}
		},
		"trash": func(p string) {
			need()
			src := r.fsMust(p, true)
			if src == root {
				r.throw(i18n.New("不能删整个工作目录", "Can't delete the whole work folder"))
			}
			if err := moveToTrash(root, src); err != nil {
				r.throw(err)
			}
		},
		"copy": func(from, to string) {
			need()
			var src string
			if name, ok := strings.CutPrefix(from, "local:"); ok {
				if r.h.FilesDir == "" || name == "" || filepath.Base(name) != name {
					r.throw(i18n.Errorf("本机文件不对：%s", "Invalid local file: %s", from))
				}
				src = filepath.Join(r.h.FilesDir, name)
			} else {
				src = r.fsMust(from, true)
			}
			dst := r.fsMust(to, false)
			if _, err := os.Lstat(dst); err == nil {
				r.throw(i18n.Errorf("%s 已经有了", "%s already exists", to))
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				r.throw(err)
			}
			if err := copyFile(src, dst); err != nil {
				r.throw(err)
			}
		},
		"toLocal": func(p string) string {
			need()
			if r.h.FilesDir == "" {
				r.throw(i18n.New("这里不能存本机文件", "Local files aren't available here"))
			}
			src := r.fsMust(p, true)
			st, err := os.Stat(src)
			if err != nil {
				r.throw(err)
			}
			if st.IsDir() {
				r.throw(i18n.Errorf("%s 是目录", "%s is a directory", p))
			}
			// 名字按「路径 + 修改时间 + 大小」算：同一个文件没改过得到同一个名字，不重复存
			sum := md5.Sum([]byte(fmt.Sprintf("%s:%d:%d", src, st.ModTime().UnixNano(), st.Size())))
			ext := strings.ToLower(filepath.Ext(src))
			if !fsExtOK(ext) {
				ext = ""
			}
			name := hex.EncodeToString(sum[:]) + ext
			dst := filepath.Join(r.h.FilesDir, name)
			if _, err := os.Stat(dst); err != nil {
				if err := os.MkdirAll(r.h.FilesDir, 0o700); err != nil {
					r.throw(err)
				}
				// 先试硬链接（同一块盘上不占空间、几个 GB 的视频也是一瞬间），不行再复制
				if err := os.Link(src, dst); err != nil {
					if err := copyFile(src, dst); err != nil {
						r.throw(err)
					}
				}
			}
			return "local:" + name
		},
		"open": func(p string, opts map[string]any) {
			need()
			if r.h.Open == nil {
				r.throw(i18n.New("这里不能用系统程序打开文件", "Opening files with system apps isn't available here"))
			}
			full := r.fsMust(p, true)
			reveal, _ := opts["reveal"].(bool)
			if err := r.h.Open(full, reveal); err != nil {
				r.throw(err)
			}
		},
	}
}

func fsClean(p string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
}

func fsExtOK(ext string) bool {
	if len(ext) < 2 || len(ext) > 7 {
		return false
	}
	for _, c := range ext[1:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (r *runner) fsMust(p string, exist bool) string {
	full, err := r.fsPath(p, exist)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			r.throw(i18n.Errorf("没有这个文件：%s", "No such file: %s", p))
		}
		r.throw(err)
	}
	return full
}

// fsPath 把相对工作目录的路径变成绝对路径，确认它在工作目录里面（软链接解开以后也在）。
// exist 时文件要存在；不要求存在时，检查最近一层存在的上级目录。
func (r *runner) fsPath(p string, exist bool) (string, error) {
	root := r.h.WorkFiles
	raw := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", i18n.Errorf("要用相对工作目录的路径：%s", "Use a path relative to the work folder: %s", p)
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", i18n.Errorf("路径里不能有 ..：%s", "Paths can't contain ..: %s", p)
		}
	}
	full := filepath.Join(root, filepath.FromSlash(fsClean(raw)))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	check := full
	for {
		real, err := filepath.EvalSymlinks(check)
		if err == nil {
			if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				return "", i18n.Errorf("%s 在工作目录外面", "%s is outside the work folder", p)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if exist {
			return "", fs.ErrNotExist
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	return full, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if st, err := in.Stat(); err == nil && st.IsDir() {
		return i18n.New("只能复制文件，不能复制目录", "Only files can be copied, not folders")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// userTrash：macOS 的废纸篓（访达里能放回）；别的系统空，挪进工作目录的 .trash/（ctx.fs.list 不列）。测试里换掉
var userTrash = func() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".Trash")
		}
	}
	return ""
}

func moveToTrash(root, src string) error {
	dir := userTrash()
	if dir == "" {
		dir = filepath.Join(root, ".trash")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(src)
	dst := filepath.Join(dir, base)
	if _, err := os.Lstat(dst); err == nil {
		ext := filepath.Ext(base)
		dst = filepath.Join(dir, strings.TrimSuffix(base, ext)+" "+time.Now().Format("150405")+ext)
	}
	return os.Rename(src, dst)
}
