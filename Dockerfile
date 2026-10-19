# Annulo 服务端版（docs/server-deploy.md）：docker build -t annulo . ；数据在 /data，业务表配了 DATABASE_URL 就存 Postgres。
# 本机版用 Electron 安装包，不用这个镜像。

FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/ third_party/
RUN go mod download
COPY . .
COPY --from=web /src/web/dist internal/webui/dist
# 版本号：不传 VERSION 时从 changelog.yaml 取最近一次正式发版的版本；最上面还有没发的 next 时带 +next（比它新、还没发版）
ARG VERSION=
RUN V="$VERSION"; if [ -z "$V" ]; then \
      V=$(awk '/^- version:/{print $3}' changelog.yaml | grep -v '^next$' | head -1); \
      [ "$(awk '/^- version:/{print $3; exit}' changelog.yaml)" = next ] && V="$V+next"; \
    fi; \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/annulo/annulo/internal/version.Version=${V:-dev}" -o /out/annulo ./cmd/annulo

FROM debian:bookworm-slim
# 助手在容器里跑命令要用的：git（项目历史）、bash、python3、node / npm（装工具、跑脚本）、curl
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git bash curl python3 python3-venv nodejs npm tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 1000 --create-home --home-dir /data/home annulo \
 && mkdir -p /data && chown -R annulo:annulo /data
COPY --from=go /out/annulo /usr/local/bin/annulo
RUN ln -s annulo /usr/local/bin/shuttle
# 要留下来的都在 /data（项目、对话、密钥、账号、上传的文件）；HOME 也在里面，git、pip --user 的配置重启不丢。
# 助手装的长期工具放 /data/tools（系统提示里说了），加进 PATH
# HTTP 8080、HTTPS 8443，映射成宿主机的 80 / 443；证书由 Annulo 自己签（设置 → 域名，internal/server/domain.go）
ENV ANNULO_MODE=server \
    ANNULO_DIR=/data \
    ANNULO_PORT=8080 \
    ANNULO_TLS_PORT=8443 \
    HOME=/data/home \
    PATH=/data/tools/bin:/data/home/.local/bin:$PATH
USER annulo
WORKDIR /data
VOLUME /data
EXPOSE 8080 8443
ENTRYPOINT ["tini", "--", "annulo", "--no-open"]
