//go:build !windows

package localfn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExec(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.ts", `
export async function run(_: any, ctx: any) {
  const echo = await ctx.exec('echo', ['a b', '$HOME;rm', 1])
  const sh = await ctx.exec('sh', ['-c', 'printf "%s" "$1" | tr a-z A-Z; echo oops >&2; exit 3', '_', 'x; echo injected'])
  const input = await ctx.exec('cat', [], { input: 'from stdin' })
  const env = await ctx.exec('sh', ['-c', 'printf "%s" "$K"'], { env: { K: ctx.secrets.get('K') } })
  const cwd = await ctx.exec('pwd', { cwd: 'sub' })
  const script = await ctx.exec('sh', ['scripts/hi.sh', 'bob'])
  const direct = await ctx.exec('./hi.sh', ['amy'], { cwd: 'scripts' })
  const noStdin = await ctx.exec('cat')
  return { echo, sh, input, env, cwd: cwd.stdout.trim(), script: script.stdout, direct: direct.stdout, noStdin }
}
export async function outside(_: any, ctx: any) { return await ctx.exec('pwd', [], { cwd: '../' }) }
export async function missing(_: any, ctx: any) { return await ctx.exec('no-such-cmd-annulo').catch((e: any) => 'caught: ' + e.message) }
export async function slow(_: any, ctx: any) { return await ctx.exec('sh', ['-c', 'sleep 30 & sleep 30'], { timeout: 300 }) }
`)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "hi.sh"), []byte("#!/bin/sh\necho hi $1\n"), 0o755)

	res, err := Run(context.Background(), host(dir, &memDB{}), "x.run", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	out := func(k string) map[string]any { return m[k].(map[string]any) }
	if s := out("echo")["stdout"]; s != "a b $HOME;rm 1\n" {
		t.Fatalf("参数要原样传、不经过 shell：%q", s)
	}
	if r := out("sh"); r["stdout"] != "X; ECHO INJECTED" || r["stderr"] != "oops\n" || fmt.Sprint(r["code"]) != "3" {
		t.Fatalf("sh -c 的结果不对：%#v", r)
	}
	if out("input")["stdout"] != "from stdin" || out("env")["stdout"] != "sk-1" || out("noStdin")["stdout"] != "" {
		t.Fatalf("input / env / 空 stdin 不对：%#v", m)
	}
	if real, _ := filepath.EvalSymlinks(filepath.Join(dir, "sub")); m["cwd"] != real && m["cwd"] != filepath.Join(dir, "sub") {
		t.Fatalf("cwd 不对：%v", m["cwd"])
	}
	if m["script"] != "hi bob\n" || m["direct"] != "hi amy\n" {
		t.Fatalf("项目里的脚本：%#v", m)
	}

	if _, err := Run(context.Background(), host(dir, &memDB{}), "x.outside", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "要在项目里") {
		t.Fatalf("cwd 出了项目应该报错：%v", err)
	}
	res, _ = Run(context.Background(), host(dir, &memDB{}), "x.missing", nil, func(Event) {})
	if s, _ := res.(string); !strings.Contains(s, "找不到命令") {
		t.Fatalf("找不到命令应该 reject：%v", res)
	}
	start := time.Now()
	if _, err := Run(context.Background(), host(dir, &memDB{}), "x.slow", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "已停止") {
		t.Fatalf("超时应该报错：%v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("超时后要连子进程一起杀掉，等了 %s", d)
	}
}

func TestCapBuffer(t *testing.T) {
	b := &capBuffer{max: 5}
	b.Write([]byte("abc"))
	if n, err := b.Write([]byte("defg")); n != 4 || err != nil || b.String() != "abcde" || !b.cut {
		t.Fatalf("截断不对：%q %v", b.String(), b.cut)
	}
}
