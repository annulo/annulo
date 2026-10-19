# 部署 Annulo 到服务器

Annulo 可以部署到你自己的服务器上 24 小时运行：定时任务、到点发布不依赖电脑开机，团队几个人用同一个网址，手机直接打开。
这里讲怎么装、怎么绑域名、怎么升级和备份。实现细节见 [docs/server-deploy.md](../docs/server-deploy.md)。

## 要准备的

- 一台 Linux 服务器（1 核 2G 起），装好 Docker（带 `docker compose`）；
- 安全组 / 防火墙放行 80、443；
- 一个域名（可以装好以后再绑）。

## 安装

```sh
git clone https://github.com/annulo/annulo
cd annulo
docker build -t annulo/annulo:latest .
cd deploy
docker compose up -d
```

不用填任何配置：数据库只在 Docker 内部网络里、不对外开端口，compose 已经配好连接。之后所有配置（域名、证书、模型、密钥、OAuth 客户端、成员、节点）都在网页的 设置 里改。

## 第一次打开

1. 浏览器打开 `http://<服务器 IP>`，第一个注册的账号是**管理员**。
2. 这时还是 http，登录密码是明文，顶部会提醒你绑域名。尽快做下一步。

## 绑域名、开 HTTPS

1. 在域名服务商那里加一条 **A 记录**，把域名（比如 `annulo.example.com`）解析到服务器 IP。
2. Annulo 里 **设置 → 域名**，填上域名（邮箱可以不填），点「绑定并申请证书」。
3. Annulo 先自检这个域名能不能访问到自己，通过就当场向 Let's Encrypt 申请证书，几秒后跳到 `https://<域名>`。

之后证书**自动续期**，不用管。设置 → 域名 里能看到到期时间、上次失败原因，能手动续期；证书出问题时管理员顶部有提醒。

自检没过时页面会说是哪里不对：解析还没生效、解析到了别的 IP、80 端口没放行，或者前面还有别的程序占着 80。
确认外面能访问、只是服务器访问不到自己的公网地址（有的云厂商是这样）时，可以点「跳过自检直接申请」。

**不需要也不要再放 Nginx / Caddy / Traefik 在前面**：证书由 Annulo 自己管，前面再挡一层，它就签不了证书。

## 加成员

**设置 → 成员**（管理员）：选角色、生成邀请链接发给对方，7 天内有效、只能用一次。

| 角色 | 能做什么 |
|---|---|
| 管理员 | 全部：成员、设置（模型、密钥、MCP、连接、域名、节点）、新建 / 删除 / 升级项目 |
| 成员 | 用助手、跑任务、改数据、跑函数；**助手能在服务器上执行命令，谨慎给** |
| 只读 | 只看后台页面和数据，不能用助手、不能改 |

## 连 creght、MCP、Google 这些账号

设置 → 连接 / MCP 里用账号授权（OAuth）时，授权完会跳回 Annulo。授权方只接受 **HTTPS 地址或本机地址**，所以：

- **先绑域名**（上一节）再连。用公网 IP 加 http 打开时，授权按钮会提示先绑域名；
- creght 也可以不走授权，用 creght 控制台里生成的**个人访问令牌**连；
- **Google**（Search Console、GA4）要用你自己的 Google OAuth 客户端：设置 → 连接 → Google 卡片上「换成自己的 Google OAuth 客户端」，
  照提示在 Google Cloud 控制台建一个「Web 应用」类型的客户端，重定向 URI 复制页面上给的地址，再把客户端 ID 和密钥填回来。

## 社媒发布、采集：连一台电脑（节点）

服务器上没有浏览器。要发社媒、采数据，找一台电脑装上 Annulo App：

1. 服务器上 **设置 → 节点** 给这台电脑生成令牌（只显示一次）；
2. 电脑上 Annulo App 的 **设置 → 节点** 填服务器地址和令牌，打开开关。

电脑开着时，服务器上的发布、采集就用它的浏览器和登录态。电脑主动连服务器，不需要公网地址。

## 升级

```sh
cd annulo && git pull
docker build -t annulo/annulo:latest .
cd deploy && docker compose up -d
```

升级时会停一下再起来（Annulo 只能跑一个实例），几秒钟。数据都在卷里，不受影响。

## 备份和恢复

要备份两样：

```sh
# 1. 数据库（业务表）
docker compose exec -T db pg_dump -U annulo annulo > annulo-db.sql
# 2. 数据目录（项目文件、对话、密钥、账号、证书、上传的文件）
docker run --rm -v deploy_annulo-data:/data -v "$PWD":/backup debian tar czf /backup/annulo-data.tgz -C /data .
```

卷名前缀是 compose 的项目名（默认是目录名 `deploy`），用 `docker volume ls` 看实际名字。
恢复时先 `docker compose down`，把 `annulo-data.tgz` 解回卷里，再 `docker compose up -d db`，`psql` 导入 `annulo-db.sql`，最后起 annulo。

## 用 Docker Swarm

同一份文件也能用：

```sh
docker swarm init   # 还没有 Swarm 的话
POSTGRES_PASSWORD=<密码> docker stack deploy -c docker-compose.yml annulo
```

- Annulo 只跑一个副本，多节点的 Swarm 要把 annulo 和 db 固定在同一台机器（文件里 `placement` 的注释），数据卷在那台机器上。
- 80 / 443 用 host 模式发布，Annulo 才能自己签证书、看到访客的真实 IP。
- Swarm 不管启动顺序：Annulo 起来时数据库还没好会自己等（最多 2 分钟），之后用到时再连。

## 和 creght 独立部署版装在同一台服务器

80 / 443 只能给一个程序。这时让 creght 占着（它要管网站的域名和证书）：

1. 删掉本文件 `annulo` 服务的 `ports`，让 Annulo 只在内部网络里，和 creght 接到同一个 Docker 网络；
2. 在 creght 管理后台「域名反代到内部服务」里绑 Annulo 的域名，内部地址填 `http://annulo:8080`，证书由 creght 签。

creght 的部署说明在 weave 仓库的 `deploy/selfhost/README.md`。

## 配置项

部署时什么都不用填，配置都在网页的 设置 里。下面几个是容器编排层面的，都有默认值，确实需要才改（写在 compose 同目录的 `.env` 里）：

| 变量 | 说明 |
|---|---|
| `HTTP_PORT` / `HTTPS_PORT` | 服务器上对外的端口，默认 80 / 443 |
| `POSTGRES_PASSWORD` | 内部数据库的密码，默认有一个（数据库不对外开端口）；要改得在第一次启动前改 |
| `ANNULO_IMAGE` | 用哪个镜像，默认 `annulo/annulo:latest` |

## 排障

- 看日志：`docker compose logs -f annulo`。
- 打不开 `http://<IP>`：安全组 / 防火墙是不是放行了 80；`docker compose ps` 看 annulo 是不是在跑。
- 业务表报「项目的数据库连不上」：`docker compose ps db` 看数据库；日志里有「等 Postgres」的原因。
- 忘了管理员密码：别的管理员在 设置 → 成员 里删掉重邀；只有一个管理员时，停掉服务、删掉数据目录里的 `users.json` 和 `sessions.json` 再起来，第一个注册的又是管理员（项目、数据都还在）。
