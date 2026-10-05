// Package brand 收拢产品的名字，以及「新旧名字都认」的规则（docs/annulo-plan.md 第 1 步）。
//
// 产品从 Shuttle 改名 Annulo。现在还按旧名字输出（写文件、发事件、给子进程的环境变量），但新旧名字都认：
// 用新名字写的项目、模板、页面在这一版就能用。第 6 步切换时改这里的输出，旧名字继续认。
//
// 和项目、模板的约定（文件名、URL 前缀、请求头、远程函数名、页面事件）旧名字永久认：已有项目和页面里写死了它们。
package brand

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Name 是对外显示的产品名（2026-10-05 起是 Annulo，以前叫 Shuttle）。
const Name = "Annulo"

// 环境变量前缀：先读新的，没有再读旧的。
const (
	envNew = "ANNULO_"
	envOld = "SHUTTLE_"
)

// Env 读环境变量 ANNULO_<key>，没设再读 SHUTTLE_<key>。
func Env(key string) string {
	if v := os.Getenv(envNew + key); v != "" {
		return v
	}
	return os.Getenv(envOld + key)
}

// EnvName 是报错里提到的那个变量名：设了哪个就说哪个，都没设说旧的（现在文档里写的是它）。
func EnvName(key string) string {
	if os.Getenv(envNew+key) != "" {
		return envNew + key
	}
	return envOld + key
}

// ChildEnv 是给子进程设的环境变量：新旧两个名字都设，子进程里不管认哪个都能读到。
func ChildEnv(key, val string) []string {
	return []string{envOld + key + "=" + val, envNew + key + "=" + val}
}

// 项目根目录声明能力版本要求的文件，按读取顺序：{"min_annulo_api": 3} 或 {"min_shuttle_api": 3}。
var ProjectFiles = []string{"annulo.json", "shuttle.json"}

// 项目的总说明（放进系统提示），按读取顺序。
var NotesFiles = []string{"ANNULO.md", "SHUTTLE.md"}

// 离线项目的标记目录（<目录>/project.json），按读取顺序。新建时写 MarkerDir。
var MarkerDirs = []string{".annulo", ".shuttle"}

// MarkerDir 是新建离线项目时写的标记目录。
const MarkerDir = ".shuttle"

// URL 前缀：页面、外壳、命令行都通过它找 App 自己的接口。两个前缀挂同一套路由，旧的永久保留。
const (
	URLPrefix    = "/_shuttle/" // 现在输出的
	URLPrefixNew = "/_annulo/"
)

// CanonicalPath 把 /_annulo/… 换成 /_shuttle/…，没有这个前缀的原样返回。路由只按旧前缀注册一份，进来时先换一下。
func CanonicalPath(p string) string {
	if strings.HasPrefix(p, URLPrefixNew) {
		return URLPrefix + p[len(URLPrefixNew):]
	}
	if p == strings.TrimSuffix(URLPrefixNew, "/") {
		return strings.TrimSuffix(URLPrefix, "/")
	}
	return p
}

// 请求头：页面和命令行带它证明请求来自 App 里（CSRF 防护），App 的响应也带它（页面靠它判断在不在 App 里）。
const (
	Header    = "X-Shuttle"
	HeaderNew = "X-Annulo"
)

// RemotePrefixes 是内置远程函数名的前缀（_shuttle.send…）。报给中转的仍用第一个旧的：手机端和站点 Func 只认它。
var RemotePrefixes = []string{"_shuttle.", "_annulo."}

// RemoteName 把 _annulo.xxx 换成 _shuttle.xxx，别的原样返回。
func RemoteName(fn string) string {
	if strings.HasPrefix(fn, "_annulo.") {
		return "_shuttle." + strings.TrimPrefix(fn, "_annulo.")
	}
	return fn
}

// CloudMarkers 是本机函数打包成云端 Func 时写在生成文件开头的标记，认出来的才删、才覆盖。写第一个。
var CloudMarkers = []string{"// shuttle:cloud ", "// annulo:cloud "}

// HasCloudMarker 判断文件内容是不是生成的。
func HasCloudMarker(b []byte) bool {
	s := string(b)
	for _, m := range CloudMarkers {
		if strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

// 数据目录（docs/annulo-plan.md 第 6 步）：ANNULO_DIR / SHUTTLE_DIR，没设就是 ~/.annulo。
// 老用户只有 ~/.shuttle 时，第一次用新版把它改名成 ~/.annulo，原地留一个指过去的软链（Windows 上是目录联接）：
// 项目里存的绝对路径、git worktree、浏览器配置、模板发布脚本写死的 ~/.shuttle 都还认得。
// 改名失败（老版本还开着、没权限）就接着用 ~/.shuttle，不挡住启动。Electron 外壳（electron/main.cjs）用同样的规则。
const (
	dataDirName    = ".annulo"
	oldDataDirName = ".shuttle"
)

var dataDirOnce struct {
	sync.Once
	dir string
}

// DataDir 是 App 的数据目录，需要时先把老的 ~/.shuttle 迁过来（一个进程只做一次）。
func DataDir() string {
	if v := Env("DIR"); v != "" {
		return v
	}
	dataDirOnce.Do(func() {
		home, _ := os.UserHomeDir()
		dataDirOnce.dir = MigrateDataDir(home)
	})
	return dataDirOnce.dir
}

// MigrateDataDir 在 home 下找数据目录：有 .annulo 用它；只有真目录 .shuttle 就改名过去、留软链；都没有用 .annulo。
func MigrateDataDir(home string) string {
	neu, old := filepath.Join(home, dataDirName), filepath.Join(home, oldDataDirName)
	if _, err := os.Stat(neu); err == nil {
		return neu
	}
	fi, err := os.Lstat(old)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return neu
	}
	if err := os.Rename(old, neu); err != nil {
		log.Printf("数据目录没能从 %s 迁到 %s，接着用老的：%v", old, neu, err)
		return old
	}
	if err := linkDir(neu, old); err != nil {
		log.Printf("在 %s 留指向 %s 的链接失败（老路径不再能用）：%v", old, neu, err)
	}
	return neu
}

// linkDir 在 link 放一个指向 target 的目录链接：Unix 是软链，Windows 是目录联接（不用管理员权限）。
func linkDir(target, link string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
	}
	return os.Symlink(target, link)
}
