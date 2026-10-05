VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
PKG     := github.com/annulo/annulo/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
# 「连接」里 Google 的 OAuth 客户端（桌面应用类型，Google Cloud Console 下载的 JSON）。文件不进仓库，
# 没有这个文件打出来的 Shuttle 连不了 Google（也可以运行时用 SHUTTLE_GOOGLE_CLIENT_ID / _SECRET）。
# 带 secret 的构建命令前面加了 @，不回显到终端。
GOOGLE_OAUTH ?= .secrets/google-oauth-client.json
LDFLAGS += $(shell test -f $(GOOGLE_OAUTH) && python3 -c 'import json,sys; c=list(json.load(open(sys.argv[1])).values())[0]; print("-X github.com/annulo/annulo/internal/connect.GoogleClientID=" + c["client_id"] + " -X github.com/annulo/annulo/internal/connect.GoogleClientSecret=" + c["client_secret"])' $(GOOGLE_OAUTH))
BIN     := bin/annulo
WEBDIST := internal/webui/dist

.PHONY: web go build dev dev-server run test clean render-config

web:
	npm --prefix web ci --silent 2>/dev/null || npm --prefix web install --silent
	npm --prefix web run build
	rm -rf $(WEBDIST); mkdir -p $(WEBDIST); cp -R web/dist/. $(WEBDIST)/
	touch $(WEBDIST)/.gitkeep

go:
	@echo "go build -o $(BIN) ./cmd/annulo"
	@go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/annulo
	@printf '#!/bin/sh\n# 老名字：shuttle 命令转给 annulo（模板、助手里写的 shuttle run 照常能用）\nexec "$$(dirname "$$0")/annulo" "$$@"\n' > bin/shuttle && chmod +x bin/shuttle

build: web go

# 前端热更新：vite 跑在 5173，/_shuttle/api 转给 dev-server
dev:
	npm --prefix web run dev

dev-server:
	go run ./cmd/annulo --no-open --web web/dist

run: build
	./$(BIN)

test:
	go test ./...
	npm --prefix web run typecheck

# 随安装包带的页面渲染配置（不连 creght 时用，internal/localsite/importmap.go）：从平台拉一份最新的快照
render-config:
	curl -fsS https://creght.cn/api/tr/system_info | python3 -c 'import json,sys; rc=json.load(sys.stdin)["render_config"]; json.dump({k: rc[k] for k in ("import_map", "dev_import_map", "ignore_import_map") if k in rc}, sys.stdout, indent=2, ensure_ascii=False, sort_keys=True)' > internal/localsite/default_render_config.json

clean:
	rm -rf bin web/dist
	find $(WEBDIST) -mindepth 1 ! -name .gitkeep -delete

# ---- Electron 桌面 App（Mac / Windows，独立版本，自带 Chromium）----
.PHONY: electron-tools electron-mac electron-mac-split electron-windows-tools electron-win win app

# 安装包不带 creght 命令行（docs/annulo-plan.md 第 5 步）：App 自己的同步用 creght-cli 的 Go 包；助手要用就用用户自己装的。
electron-tools: web
	@mkdir -p dist/electron/arm64 dist/electron/amd64 dist/electron/bin
	@for arch in arm64 amd64; do \
		GOOS=darwin GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o dist/electron/$$arch/annulo ./cmd/annulo || exit 1; \
		printf '#!/bin/sh\nexec "$$(dirname "$$0")/annulo" "$$@"\n' > dist/electron/$$arch/shuttle && chmod +x dist/electron/$$arch/shuttle; \
	done
	lipo -create dist/electron/arm64/annulo dist/electron/amd64/annulo -output dist/electron/bin/annulo
	cp dist/electron/arm64/shuttle dist/electron/bin/shuttle # 老名字：shuttle 命令转给 annulo

electron-mac: electron-tools
	npm --prefix electron ci --no-audit --no-fund
	npm --prefix electron run test
	npm --prefix electron run pack:mac

electron-mac-split: electron-tools
	@mkdir -p dist/electron/mac-tools/arm64/bin dist/electron/mac-tools/x64/bin
	cp dist/electron/arm64/annulo dist/electron/arm64/shuttle dist/electron/mac-tools/arm64/bin/
	cp dist/electron/amd64/annulo dist/electron/amd64/shuttle dist/electron/mac-tools/x64/bin/
	npm --prefix electron ci --no-audit --no-fund
	npm --prefix electron run pack:mac:split

electron-windows-tools: web
	@for spec in x64:amd64 arm64:arm64; do \
		arch=$${spec%%:*}; goarch=$${spec##*:}; d=$(CURDIR)/dist/electron/windows/$$arch/bin; mkdir -p $$d; rm -f $$d/creght.exe; \
		GOOS=windows GOARCH=$$goarch go build -trimpath -ldflags="$(LDFLAGS)" -o $$d/annulo.exe ./cmd/annulo && \
		cp $$d/annulo.exe $$d/shuttle.exe || exit 1; \
	done

electron-win: electron-windows-tools
	npm --prefix electron ci --no-audit --no-fund
	npm --prefix electron run test
	npm --prefix electron run pack:win

win: electron-win
app: electron-mac-split
