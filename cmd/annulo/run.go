package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/config"
)

// annulo run <文件.函数> --input '{…}'：执行运营后台的本机函数（local/*.ts），和页面上按按钮是同一条路。
// 调的是本机正在跑的 Shuttle（函数要用它的登录态、业务表、密钥）。agent 写完函数用它试跑。
func runCmd() *cobra.Command {
	var input string
	cmd := &cobra.Command{
		Use:   "run <文件.函数>",
		Short: "执行运营后台的本机函数（local/*.ts），比如 annulo run geo.check --input '{\"channel_id\":\"...\"}'",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			base := fmt.Sprintf("http://%s/_shuttle/api/", cfg.Addr())
			if len(args) == 0 {
				return listFunctions(base)
			}
			var in any
			if input != "" {
				if strings.HasPrefix(input, "@") {
					b, err := os.ReadFile(input[1:])
					if err != nil {
						return err
					}
					input = string(b)
				}
				if err := json.Unmarshal([]byte(input), &in); err != nil {
					return i18n.Errorf("--input 不是合法 JSON：%w", "--input isn't valid JSON: %w", err)
				}
			}
			return runFunction(base, args[0], in)
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "传给函数的 input（JSON；@文件 表示从文件读）")
	return cmd
}

func shuttleReq(method, url string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("X-Shuttle", "1")
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, i18n.Errorf("连不上本机的 Annulo（%s）：先打开 Annulo App 或运行 shuttle", "Can't reach Annulo on this machine (%s): open the Annulo app or run shuttle first", url)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, errors.New(e.Error)
	}
	return resp, nil
}

func listFunctions(base string) error {
	resp, err := shuttleReq("GET", base+"local/functions", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		List []struct{ Name, File string }
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.List) == 0 {
		fmt.Println(i18n.T("运营后台还没有本机函数（local/*.ts）", "The back office has no local functions yet (local/*.ts)"))
	}
	for _, f := range out.List {
		fmt.Printf("%-28s %s\n", f.Name, f.File)
	}
	return nil
}

// runFunction 读 SSE：progress / log 打到 stderr，结果（JSON）打到 stdout，出错时退出码 1。
func runFunction(base, fn string, input any) error {
	// 助手在对话里跑的：带上对话 id（Annulo 给助手的 bash 设了 ANNULO_CHAT_ID），本机函数里是 ctx.chat_id
	resp, err := shuttleReq("POST", base+"local/run", map[string]any{"fn": fn, "input": input, "chat_id": brand.Env("CHAT_ID")})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "progress", "log":
			var m map[string]any
			if json.Unmarshal(ev.Data, &m) == nil && m["message"] != nil {
				fmt.Fprintf(os.Stderr, "  %s %v\n", map[string]string{"progress": "·", "log": "›"}[ev.Type], m["message"])
			} else {
				fmt.Fprintf(os.Stderr, "  · %s\n", ev.Data)
			}
		case "result":
			var r struct {
				Value json.RawMessage `json:"value"`
				Ms    int64           `json:"ms"`
			}
			json.Unmarshal(ev.Data, &r)
			var pretty bytes.Buffer
			if json.Indent(&pretty, r.Value, "", "  ") != nil {
				pretty.Write(r.Value)
			}
			fmt.Println(pretty.String())
			fmt.Fprintf(os.Stderr, i18n.T("  ✓ %s 完成，用时 %.1f 秒\n", "  ✓ %s done in %.1fs\n"), fn, float64(r.Ms)/1000)
			return nil
		case "error":
			var e struct {
				Message string `json:"message"`
				Stack   string `json:"stack"`
			}
			json.Unmarshal(ev.Data, &e)
			if e.Stack != "" {
				fmt.Fprintln(os.Stderr, e.Stack)
			}
			return i18n.Errorf("%s 失败：%s", "%s failed: %s", fn, e.Message)
		}
	}
	return i18n.New("连接断了，没拿到结果", "The connection dropped before a result came back")
}
