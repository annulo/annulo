package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
)

// annulo mcp list / add / remove：助手给 Annulo 接 MCP。改的是本机正在跑的 Annulo 的 MCP 配置（~/.annulo/mcp.json），
// 和 设置 → MCP 页面保存是同一个接口：校验、重连、刷新助手的工具。参数照 claude mcp add 的写法。
func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: i18n.T("管理 Annulo 连的 MCP server（list / add / remove）", "Manage the MCP servers Annulo connects to (list / add / remove)")}
	list := &cobra.Command{Use: "list", Short: i18n.T("列出 MCP server 和连接状态", "List MCP servers and their status"), Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			st, err := mcpGet()
			if err != nil {
				return err
			}
			printMCP(st, "")
			return nil
		}}

	var transport string
	var headers, envs []string
	var force bool
	add := &cobra.Command{
		Use: "add <名字> <url> | add <名字> -- <命令> [参数…]",
		Short: i18n.T("加一个 MCP server：远程的给地址，本地的给启动命令",
			"Add an MCP server: a URL for a remote one, a command for a local one"),
		Example: `  annulo mcp add notion https://mcp.notion.com/mcp
  annulo mcp add github https://api.githubcopilot.com/mcp/ --header 'Authorization: Bearer ${GITHUB_TOKEN}'
  annulo mcp add playwright -- npx @playwright/mcp@latest
  annulo mcp add fs --env ROOT=/tmp -- npx -y @modelcontextprotocol/server-filesystem /tmp`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			name, rest := args[0], args[1:]
			sc := map[string]any{}
			if transport == "" {
				transport = "stdio"
				if strings.HasPrefix(rest[0], "http://") || strings.HasPrefix(rest[0], "https://") {
					transport = "http"
				}
			}
			switch transport {
			case "http", "sse":
				if len(rest) != 1 {
					return i18n.New("远程 MCP 只给一个地址", "A remote MCP takes exactly one URL")
				}
				sc["type"], sc["url"] = transport, rest[0]
				if len(headers) > 0 {
					h := map[string]string{}
					for _, kv := range headers {
						k, v, ok := strings.Cut(kv, ":")
						if !ok {
							return i18n.Errorf("--header 要写成 '名字: 值'：%s", "--header must look like 'Name: value': %s", kv)
						}
						h[strings.TrimSpace(k)] = strings.TrimSpace(v)
					}
					sc["headers"] = h
				}
			case "stdio":
				sc["command"] = rest[0]
				if len(rest) > 1 {
					sc["args"] = rest[1:]
				}
				if len(envs) > 0 {
					e := map[string]string{}
					for _, kv := range envs {
						k, v, ok := strings.Cut(kv, "=")
						if !ok {
							return i18n.Errorf("--env 要写成 名字=值：%s", "--env must look like NAME=value: %s", kv)
						}
						e[k] = v
					}
					sc["env"] = e
				}
			default:
				return i18n.New("--transport 只能是 http / sse / stdio", "--transport must be http / sse / stdio")
			}
			st, err := mcpGet()
			if err != nil {
				return err
			}
			cfg, servers, err := mcpParse(st.Config)
			if err != nil {
				return err
			}
			if _, ok := servers[name]; ok && !force {
				return i18n.Errorf("已经有叫 %s 的 MCP 了：要换掉就加 --force，或者先 annulo mcp remove %s", "There's already an MCP named %s: add --force to replace it, or run annulo mcp remove %s first", name, name)
			}
			b, _ := json.Marshal(sc)
			servers[name] = b
			if _, err := mcpSave(cfg, servers); err != nil {
				return err
			}
			printMCP(mcpWait(name), name)
			return nil
		},
	}
	add.Flags().StringVarP(&transport, "transport", "t", "", "http / sse / stdio（不写时按第二个参数猜：http(s):// 开头是 http，否则是本地命令）")
	add.Flags().StringArrayVarP(&headers, "header", "H", nil, "远程 MCP 的请求头 'Authorization: Bearer ${密钥名}'（可以写多个；${名字} 用 设置 → 密钥 里的值替换）")
	add.Flags().StringArrayVarP(&envs, "env", "e", nil, "本地 MCP 的环境变量 名字=值（可以写多个；值里的 ${名字} 同样替换成密钥）")
	add.Flags().BoolVar(&force, "force", false, "同名的已经有了也换掉")

	remove := &cobra.Command{Use: "remove <名字>", Short: i18n.T("删掉一个 MCP server", "Remove an MCP server"), Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			st, err := mcpGet()
			if err != nil {
				return err
			}
			cfg, servers, err := mcpParse(st.Config)
			if err != nil {
				return err
			}
			if _, ok := servers[args[0]]; !ok {
				return i18n.Errorf("没有叫 %s 的 MCP", "No MCP named %s", args[0])
			}
			delete(servers, args[0])
			if _, err := mcpSave(cfg, servers); err != nil {
				return err
			}
			fmt.Println(i18n.T("已删掉 ", "Removed ") + args[0])
			return nil
		}}
	cmd.AddCommand(list, add, remove)
	return cmd
}

type mcpState struct {
	Config  string `json:"config"`
	File    string `json:"file"`
	Servers []struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		Status    string `json:"status"`
		Error     string `json:"error"`
		AuthURL   string `json:"auth_url"`
		Tools     []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
	} `json:"servers"`
}

func mcpURL(p string) string {
	cfg, err := config.Load()
	addr := "127.0.0.1:0"
	if err == nil {
		addr = cfg.Addr()
	}
	return fmt.Sprintf("http://%s/_shuttle/api/settings/mcp%s", addr, p)
}

func mcpGet() (*mcpState, error) {
	resp, err := shuttleReq("GET", mcpURL(""), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st mcpState
	return &st, json.NewDecoder(resp.Body).Decode(&st)
}

// mcpParse 拆出 mcpServers，别的字段（以后加的）原样保留
func mcpParse(raw string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	cfg, servers := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, nil, i18n.Errorf("mcp.json 不是合法 JSON：%w", "mcp.json isn't valid JSON: %w", err)
		}
	}
	if s, ok := cfg["mcpServers"]; ok {
		if err := json.Unmarshal(s, &servers); err != nil {
			return nil, nil, i18n.Errorf("mcp.json 的 mcpServers 不对：%w", "mcp.json's mcpServers is invalid: %w", err)
		}
	}
	return cfg, servers, nil
}

func mcpSave(cfg, servers map[string]json.RawMessage) (*mcpState, error) {
	cfg["mcpServers"], _ = json.Marshal(servers)
	b, _ := json.Marshal(cfg)
	var pretty bytes.Buffer
	json.Indent(&pretty, b, "", "  ")
	resp, err := shuttleReq("PUT", mcpURL(""), map[string]string{"config": pretty.String()})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st mcpState
	return &st, json.NewDecoder(resp.Body).Decode(&st)
}

// mcpWait 等刚加的 server 连好（最多 30 秒）：连接在 App 里后台进行
func mcpWait(name string) *mcpState {
	var st *mcpState
	for i := 0; i < 60; i++ {
		s, err := mcpGet()
		if err == nil {
			st = s
			for _, sv := range s.Servers {
				if sv.Name == name && sv.Status != "connecting" {
					return st
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return st
}

// printMCP 打印连接状态；only 不空时只打这一个，并说下一步怎么做
func printMCP(st *mcpState, only string) {
	if st == nil {
		return
	}
	if len(st.Servers) == 0 {
		fmt.Println(i18n.T("还没有 MCP server", "No MCP servers yet"))
	}
	for _, s := range st.Servers {
		if only != "" && s.Name != only {
			continue
		}
		on := 0
		for _, t := range s.Tools {
			if t.Enabled {
				on++
			}
		}
		fmt.Printf("%-20s %-6s %-11s %s\n", s.Name, s.Transport, s.Status, fmt.Sprintf(i18n.T("工具 %d/%d 个开着", "%d/%d tools on"), on, len(s.Tools)))
		if s.Error != "" {
			fmt.Println("  " + s.Error)
		}
		if only == "" {
			continue
		}
		switch s.Status {
		case "connected":
			fmt.Println(i18n.T("  连上了。从下一条消息开始工具是 mcp__"+s.Name+"__<工具>；本机函数里用 ctx.mcp('"+s.Name+"', 工具, 参数)。",
				"  Connected. From the next message its tools are mcp__"+s.Name+"__<tool>; in local functions use ctx.mcp('"+s.Name+"', tool, args)."))
		case "needs_auth":
			fmt.Println(i18n.T("  要用户授权：让用户打开下面的地址登录（或在 设置 → MCP 里点授权），授权完自动连上：",
				"  Needs the user to authorize: have them open this URL (or click Authorize in Settings → MCP); it connects once done:"))
			fmt.Println("  " + s.AuthURL)
		case "connecting":
			fmt.Println(i18n.T("  还在连，过一会儿 annulo mcp list 再看", "  Still connecting; check again later with annulo mcp list"))
		case "failed":
			fmt.Println(i18n.T("  没连上：按上面的报错改，再 annulo mcp add "+s.Name+" … --force", "  Failed: fix it per the error above and rerun annulo mcp add "+s.Name+" … --force"))
			if s.Transport == "stdio" {
				fmt.Println(i18n.T("  命令的输出在 ", "  The command's output is in ") + logHint(st.File, s.Name))
			}
		}
	}
}

func logHint(file, name string) string {
	return filepath.Join(filepath.Dir(file), "logs", "mcp-"+name+".log")
}
