package main

import (
	"fmt"
	"github.com/annulo/annulo/internal/config"
	"github.com/spf13/cobra"
	"io"
	"net/url"
	"os"
)

func logsCmd() *cobra.Command {
	var fn, id string
	var limit int
	cmd := &cobra.Command{Use: "logs", Short: "查询当前项目的本机函数运行日志", RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		q := url.Values{"fn": {fn}, "id": {id}, "limit": {fmt.Sprint(limit)}}
		resp, err := shuttleReq("GET", fmt.Sprintf("http://%s/_shuttle/api/local/logs?%s", cfg.Addr(), q.Encode()), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.Copy(os.Stdout, resp.Body)
		return err
	}}
	cmd.Flags().StringVar(&fn, "fn", "", "按函数名筛选，例如 social.publish")
	cmd.Flags().StringVar(&id, "id", "", "读取某次运行的完整 JSONL 日志")
	cmd.Flags().IntVar(&limit, "limit", 20, "最近多少次运行，最多 100")
	return cmd
}
