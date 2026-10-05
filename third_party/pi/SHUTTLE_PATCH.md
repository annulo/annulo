# Shuttle 对 sky-valley/pi v0.87.25-0.20260928092708-07d479e4780d 的补丁

来源：github.com/sky-valley/pi v0.87.25-0.20260928092708-07d479e4780d（MIT），固定上游 main 提交 `07d479e4780dbd8152a333696f40a99108965289`（2026-09-28）。这是尚未发布的版本。去掉了上游测试和文档，其余原样。本地保留 `coding/shuttle_patch_test.go` 验证这两处补丁。

改动只有两处，都是为了让 Shuttle 自己管理 skill：

1. `coding/session.go`：`SessionOptions.Skills *[]Skill`。非 nil 时就是会话的全部 skill，
   不再扫描 `~/.pi/agent/skills`、`~/.agents/skills` 和项目目录。
2. `coding/resources.go`：导出 `LoadSkillsFromDir`，按 pi 的规则解析一个 skill 目录。

升级 pi 时：换成新版本源码，重新应用这两处改动（或者等上游支持后去掉 replace）。

本次升级包含模型运行时、provider 流式解析与超时/错误处理、会话记录持久化等上游修复；依然只保留以上两处本地补丁。
