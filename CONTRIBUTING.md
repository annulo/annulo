# Contributing to Annulo

Thanks for helping. A few things make reviews quick:

## Set up

```sh
make dev-server   # Go server on 127.0.0.1:7799
make dev          # web UI with hot reload on 5173
make test         # go test ./... and the web type check
npm --prefix electron test
```

## What belongs in Annulo

Annulo provides **primitives**: the runtime for local functions (`ctx.db`, `ctx.fetch`, `ctx.secrets`, `ctx.mcp`, `ctx.llm`…), tables, page rendering, schedules, project git, template upgrades, connections and the assistant. Anything specific to one business or one platform belongs in a project or a template, not here.

Before adding something, ask: would it still make sense for a different industry or a different platform? If not, it's a template's job. If the assistant can already build it with existing primitives, improve the prompt or a skill instead.

When you add or change a primitive, bump `API` in `internal/version/api.go` and note what changed; templates declare the version they need in `annulo.json` (`min_annulo_api`).

## Pull requests

- Keep changes focused; one topic per PR.
- Add tests for behavior changes (`go test ./...` must pass).
- UI text is Chinese-first with English in `web/src/lib/i18n.en.ts`.
- By contributing you agree your work is licensed under Apache-2.0.
