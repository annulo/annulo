package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
)

// annulo template：看当前项目基于模板的哪一版、有哪些新版本；annulo template upgrade 升级（和 设置 → 项目 里点「升级」是同一条路）。
// 用户让助手「帮我升级」时用它。有冲突时合并停在进行中，助手照 git.md 解决冲突、提交就完成了。
func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "看当前项目的模板版本和更新说明；annulo template upgrade 升级到最新",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			base, err := apiBase()
			if err != nil {
				return err
			}
			return showTemplate(base)
		},
	}
	var to int
	up := &cobra.Command{
		Use:   "upgrade",
		Short: "把当前项目升级到模板的最新版（--to 指定版本号）：三方合并，项目自己改过的地方保留，冲突的文件列出来",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			base, err := apiBase()
			if err != nil {
				return err
			}
			return upgradeTemplate(base, to)
		},
	}
	up.Flags().IntVar(&to, "to", 0, "升级到哪个版本号（不填是最新）")
	cmd.AddCommand(up)
	return cmd
}

func apiBase() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s/_shuttle/api/", cfg.Addr()), nil
}

func showTemplate(base string) error {
	resp, err := shuttleReq("GET", base+"template", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var t struct {
		FromTemplate  bool   `json:"from_template"`
		Name          string `json:"name"`
		Base          int    `json:"base"`
		BaseLabel     string `json:"base_label"`
		Latest        int    `json:"latest"`
		LatestLabel   string `json:"latest_label"`
		API           int    `json:"api"`
		Requires      int    `json:"requires"`
		Merging       bool   `json:"merging"`
		Conflicts     []string
		VersionsError string `json:"versions_error"`
		Updates       []struct {
			No    int    `json:"no"`
			Label string `json:"label"`
			Note  string `json:"note"`
		} `json:"updates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return err
	}
	if !t.FromTemplate {
		fmt.Println(i18n.T("这个项目不是从模板复制的，没有模板可以升级", "This project wasn't copied from a template, so there's no template to upgrade"))
		return nil
	}
	label := func(no int, l string) string {
		if l != "" {
			return l
		}
		return i18n.Tf("第 %d 版", "version %d", no)
	}
	fmt.Println(i18n.Tf("模板：%s，当前基于 %s", "Template: %s, based on %s", t.Name, label(t.Base, t.BaseLabel)))
	if t.VersionsError != "" {
		fmt.Println(t.VersionsError)
	}
	if t.Merging {
		fmt.Println(i18n.Tf("上一次升级还没合并完，这些文件有冲突：%s（解决后提交就完成）", "The last upgrade isn't finished; these files have conflicts: %s (resolve them, then commit)", strings.Join(t.Conflicts, ", ")))
	}
	if len(t.Updates) == 0 {
		fmt.Println(i18n.T("已经是最新版", "Already up to date"))
		return nil
	}
	fmt.Println(i18n.Tf("最新 %s，新版本：", "Latest %s; new versions:", label(t.Latest, t.LatestLabel)))
	for _, u := range t.Updates {
		fmt.Printf("  %s  %s\n", label(u.No, u.Label), u.Note)
	}
	if t.Requires > t.API {
		fmt.Println(i18n.Tf("最新版要 Annulo 能力版本 %d，这台是 %d：先更新 Annulo 再升级", "The latest version needs Annulo capability version %d; this one has %d: update Annulo first", t.Requires, t.API))
	} else {
		fmt.Println(i18n.T("升级：annulo template upgrade", "To upgrade: annulo template upgrade"))
	}
	return nil
}

// versionLabels 是版本号 → 给人看的名字（git 模板是 tag，比如 v0.5.1）；查不到时用「第 N 版」
func versionLabels(base string) func(int) string {
	names := map[int]string{}
	if resp, err := shuttleReq("GET", base+"template", nil); err == nil {
		var t struct {
			Base      int    `json:"base"`
			BaseLabel string `json:"base_label"`
			Updates   []struct {
				No    int    `json:"no"`
				Label string `json:"label"`
			} `json:"updates"`
		}
		json.NewDecoder(resp.Body).Decode(&t)
		resp.Body.Close()
		names[t.Base] = t.BaseLabel
		for _, u := range t.Updates {
			names[u.No] = u.Label
		}
	}
	return func(no int) string {
		if l := names[no]; l != "" {
			return l
		}
		return i18n.Tf("第 %d 版", "version %d", no)
	}
}

func upgradeTemplate(base string, to int) error {
	label := versionLabels(base)
	resp, err := shuttleReq("POST", base+"template/upgrade", map[string]any{"to": to, "chat_id": brand.Env("CHAT_ID")})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Status    string   `json:"status"`
			From      int      `json:"from"`
			To        int      `json:"to"`
			Files     []string `json:"files"`
			Conflicts []string `json:"conflicts"`
		} `json:"result"`
		Plugins       []string `json:"plugins"`
		PushError     string   `json:"push_error"`
		PushConflicts []string `json:"push_conflicts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	r := out.Result
	switch r.Status {
	case "up_to_date":
		fmt.Println(i18n.T("已经是最新版，不用升级", "Already up to date"))
	case "merged":
		fmt.Println(i18n.Tf("✓ 已从 %s 升级到 %s，合并干净，改到 %d 个文件", "✓ Upgraded from %s to %s cleanly; %d files changed", label(r.From), label(r.To), len(r.Files)))
	case "conflict":
		fmt.Println(i18n.Tf("从 %s 升级到 %s：合并停在进行中，这些文件有冲突（<<<<<<< HEAD / >>>>>>> template）：", "Upgrading from %s to %s: the merge is in progress; these files have conflicts (<<<<<<< HEAD / >>>>>>> template):", label(r.From), label(r.To)))
		for _, f := range r.Conflicts {
			fmt.Println("  " + f)
		}
		fmt.Println(i18n.T("照 annulo skill 的 git.md 解决冲突（两边的改动都要留），改完提交（annulo push）就完成升级", "Resolve them as described in the annulo skill's git.md (keep both sides' changes), then commit (annulo push) to finish the upgrade"))
	default:
		fmt.Println(r.Status)
	}
	if len(out.Plugins) > 0 {
		fmt.Println(i18n.Tf("装上 / 升级了插件：%s", "Installed / upgraded plugins: %s", strings.Join(out.Plugins, ", ")))
	}
	if out.PushError != "" {
		fmt.Println(i18n.Tf("推到预览失败：%s", "Pushing to the preview failed: %s", out.PushError))
	}
	if len(out.PushConflicts) > 0 {
		fmt.Println(i18n.Tf("推到预览时和远端改动有冲突：%s", "Pushing to the preview hit conflicts with remote changes: %s", strings.Join(out.PushConflicts, ", ")))
	}
	return nil
}
