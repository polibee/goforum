# 34 一键部署脚本与 Docker

> 面向新服务器的可复制部署路径：裸机 systemd 与 Docker Compose 各一条命令。脚本负责构建、密钥生成、服务注册与健康检查；安装向导负责创建超管。

## 1. 两种方式对比

| 方式 | 适用 | 依赖 | 数据目录 | 升级 |
|---|---|---|---|---|
| 裸机 systemd | 单机 VPS、已有 Nginx/Caddy | Go 1.27+、Node 20+、systemd | `/opt/goforum/data` | `git pull && sudo ./scripts/deploy.sh --upgrade` |
| Docker Compose | 希望环境隔离、快速迁移 | Docker Engine + Compose v2 | 命名卷 `goforum-data` | `git pull && ./scripts/docker-deploy.sh` |

两种方式都**不会**自动创建管理员账号：首次启动进入 `/install`，由安装向导创建超管。生产环境需要 HTTPS 反向代理，参考 [13 部署与运维](./13-部署与运维.md)。

## 2. 裸机一键部署

### 2.1 前置要求

- Linux + systemd（Debian/Ubuntu/RHEL/Alma 等）
- Go 1.27+、Node.js 20+、npm、openssl、curl 或 wget
- root 或 sudo
- 80/443 由 Nginx/Caddy 使用；应用默认只监听 `127.0.0.1:8080`

检查：

```bash
go version
node --version
npm --version
```

### 2.2 执行

```bash
git clone https://github.com/polibee/goforum.git
cd goforum

# 默认：/opt/goforum、goforum 用户、127.0.0.1:8080
sudo ./scripts/deploy.sh

# 推荐：同时写入公开 URL（必须是 https）
sudo ./scripts/deploy.sh --url https://forum.example.com
```

已有编译产物、服务器没有 Node/Go 时：

```bash
# 在本地或 CI 构建：CGO_ENABLED=0 go build -o bin/goforum ./cmd/goforum
sudo ./scripts/deploy.sh --binary ./bin/goforum --url https://forum.example.com
```

### 2.3 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--dir PATH` | `/opt/goforum` | 安装目录，不能含空格 |
| `--user NAME` | `goforum` | systemd 运行用户，不存在会自动创建 |
| `--addr HOST:PORT` | `127.0.0.1:8080` | 监听地址；公网请保持回环并由反代转发 |
| `--url URL` | 空 | 写入 `APP_URL`；生产环境必须 `https://` |
| `--env ENV` | `production` | 本地调试可传 `development`（会启用 AutoMigrate 且跳过 TOTP 强制） |
| `--binary PATH` | 空 | 跳过前后端构建，直接安装二进制 |
| `--version VER` | git describe | 注入 `main.version` |
| `--upgrade` | 关闭 | 重启前执行 `goforum migrate` |
| `--no-service` | 关闭 | 只安装文件，不写 systemd 单元 |

### 2.4 脚本做了什么

1. `npm ci && npm run build` 生成 Tailwind/esbuild 资源。
2. `CGO_ENABLED=0 go build` 生成单二进制（模板与静态资源已内嵌）。
3. 创建 `goforum` 系统用户与目录：
   ```
   /opt/goforum/
     goforum              # 二进制
     .env                 # APP_KEY、GF_BACKUP_KEY、HTTP_ADDR（0600）
     configs/goforum.yaml # 安装向导写入（0600）
     data/goforum.db      # SQLite
     data/uploads/        # 上传
     backups/             # 备份
   ```
4. 首次运行生成 `APP_KEY` 与 `GF_BACKUP_KEY`，写入 `.env`。
5. 写入 `/etc/systemd/system/goforum.service`：
   - `ProtectSystem=strict`，仅 `data/`、`backups/`、`configs/` 可写；
   - `NoNewPrivileges`、`PrivateTmp`、`ProtectHome`；
   - `Restart=on-failure`。
6. 写入 `/usr/local/bin/goforum` 管理命令：自动切换运行用户、加载 `.env`、固定 `GF_CONFIG`。
7. 启动服务并轮询 `/healthz`，失败时打印最近 30 行日志。

systemd 单元中的关键环境：

```ini
Environment=GF_ENV_FILE=/opt/goforum/.env
Environment=GF_CONFIG=/opt/goforum/configs/goforum.yaml
EnvironmentFile=-/opt/goforum/.env
```

> 安装向导里选择的数据库会同时写回 `.env` 的末尾（`DB_CONNECTION` / `DB_DSN`），确保重启后仍使用同一个库。`.env` 优先于 YAML。

### 2.5 安装完成后

1. 本机打开 `http://127.0.0.1:8080/install`；远程服务器配置反向代理，或先建立 SSH 隧道：
   ```bash
   ssh -L 8080:127.0.0.1:8080 root@<server>
   ```
2. 填写站点名称、公开 URL、数据库、管理员账号。
3. 超管登录后立即绑定 TOTP，并保存 10 张恢复码。
4. 在「设置 → 邮件」配置 SMTP/Resend，否则找回密码与验证邮件不可用。
5. 配置每日备份，并把 `APP_KEY`、`GF_BACKUP_KEY` 离线保存。

### 2.6 日常运维

```bash
systemctl status goforum
journalctl -u goforum -f

sudo goforum backup
sudo goforum migrate
sudo goforum version

# 升级代码与二进制
git pull
sudo ./scripts/deploy.sh --upgrade
```

`/usr/local/bin/goforum` 会自动以 `goforum` 用户执行，避免数据库与上传文件被 root 拥有。

### 2.7 反向代理

应用默认只监听回环。Nginx 最小配置：

```nginx
server {
  listen 443 ssl http2;
  server_name forum.example.com;

  client_max_body_size 32m;

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 60s;
  }

  location /api/v1/notifications/stream {
    proxy_pass http://127.0.0.1:8080;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 1h;
    proxy_set_header Connection "";
  }
}
```

同时在 `.env` 增加可信代理，否则限流与审计看到的都是反代 IP：

```dotenv
HTTP_TRUSTED_PROXIES=127.0.0.1
```

修改后 `sudo systemctl restart goforum`。

## 3. Docker 一键部署

### 3.1 前置要求

- Docker Engine 24+ 与 Compose v2（`docker compose version`）
- 能拉取 `node:22-alpine`、`golang:1.27-alpine`、`alpine:3.22`
- 端口 8080 可用（或 `--port` 指定）

### 3.2 执行

```bash
git clone https://github.com/polibee/goforum.git
cd goforum

./scripts/docker-deploy.sh
# 或
./scripts/docker-deploy.sh --url https://forum.example.com --port 8080
```

脚本会：

1. 检查 Docker 守护进程与 Compose。
2. 首次生成 `goforum.env`（0600），包含 `APP_KEY`、`GF_BACKUP_KEY`、监听与日志配置。
3. `docker compose up -d --build` 构建并启动。
4. 轮询容器健康状态，失败时输出最近 80 行日志。
5. 打印安装向导地址与常用命令。

国内网络可指定 Go 模块代理（Compose 会把 `GOPROXY` 传给构建阶段）：

```bash
GOPROXY=https://goproxy.cn,direct ./scripts/docker-deploy.sh --url https://forum.example.com
```

镜像构建分三段：

```text
node:22-alpine      → Tailwind + esbuild 资源
golang:1.27-alpine  → CGO_ENABLED=0 单二进制
alpine:3.22         → 非 root 运行，ca-certificates + tzdata
```

运行容器以 `goforum`（UID 10001）执行；入口脚本只修正 `/data` 目录属主，然后 `su-exec` 降权。

### 3.3 数据与配置

| 路径 | 说明 |
|---|---|
| 命名卷 `goforum-data` | `/data/goforum.db`、`/data/uploads`、`/data/goforum.yaml`、`/data/backups` |
| `goforum.env`（宿主机） | `APP_KEY`、`GF_BACKUP_KEY`、`HTTP_ADDR`、日志、端口；**不要提交** |
| 容器 `/data/.env` | 默认不存在；如手动创建，会参与配置覆盖（进程环境优先于该文件） |

Compose 把 `goforum.env` 作为 `env_file` 注入进程环境；配置加载顺序为：

```text
内置默认值 → configs（GF_CONFIG=/data/goforum.yaml）→ 进程环境（env_file / GF_*）
```

数据库默认是 `/data/goforum.db`（容器工作目录为 `/`，默认 DSN `./data/goforum.db`）。安装向导里切换数据库后，选择会写入 `/data/goforum.yaml` 并持久化。

### 3.4 日常运维

```bash
docker compose logs -f goforum
docker compose restart goforum

# 备份到 /data/backups/<timestamp>
docker compose exec goforum /app/goforum backup

# 查看版本 / 手动迁移
docker compose exec goforum /app/goforum version
docker compose exec goforum /app/goforum migrate

# 停止（保留数据）
docker compose down

# 停止并删除数据卷（不可恢复）
docker compose down -v
```

升级：

```bash
git pull
./scripts/docker-deploy.sh
# 需要迁移时：
docker compose exec goforum /app/goforum migrate
```

### 3.5 反向代理与端口

Compose 默认把 `8080` 发布到所有网卡。生产环境建议：

- 云安全组/防火墙只放行 443；
- Nginx/Caddy 反代到 `127.0.0.1:8080`；
- 或把 Compose 端口改为 `127.0.0.1:8080:8080`，只允许本机反代访问。

修改 `docker-compose.yml`：

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

反代配置与裸机相同，另需设置 `HTTP_TRUSTED_PROXIES` 为反代地址（Compose 网络中通常是反代容器网段）。

### 3.6 使用外部数据库

Docker 默认 SQLite。使用 PostgreSQL/MySQL 时：

1. 启动数据库并创建库与账号。
2. 打开安装向导，在数据库字段选择 `postgres`/`mysql` 并填写 DSN。
3. 安装向导把选择写入 `/data/goforum.yaml`，重启后仍然生效。
4. 若在 `goforum.env` 中写 `DB_CONNECTION` / `DB_DSN`，它会覆盖 YAML；两者只保留一处。

> SQLite 只允许单实例。多实例必须使用 PostgreSQL/MySQL + Redis + 共享对象存储；当前上传仍为本地磁盘。

## 4. 密钥与安全

- `APP_KEY`：AES-256-GCM 主密钥，加密设置中的 SMTP/支付/OAuth 密钥与管理员 TOTP。**丢失后无法解密**。
- `GF_BACKUP_KEY`：备份加密密钥，必须与备份文件分开保存。
- 两个密钥都由脚本用 `openssl rand -base64 32` 生成，只写入 `.env` / `goforum.env`（0600）。
- 不要把 `.env`、`goforum.env`、数据库、备份提交到 Git；仓库的 `.gitignore` 与 `.dockerignore` 已排除。
- 首次安装后建议把密钥复制到密码管理器，再考虑从服务器删除明文副本（删除前确认备份与恢复流程）。
- 公网必须 HTTPS；生产环境 `APP_URL` 必须是 `https://`，否则启动校验会拒绝。

## 5. 故障排查

| 现象 | 排查 |
|---|---|
| 裸机脚本提示缺少 go/npm | 安装 Go 1.27+ 与 Node 20+，或使用 `--binary` |
| 服务启动失败 | `journalctl -u goforum -n 100 --no-pager` |
| 健康检查失败 | 确认 `HTTP_ADDR` 端口未被占用；查看 `data/` 权限 |
| 安装向导 500 | 检查 `configs/` 是否可写、数据库 DSN 是否正确 |
| 安装后数据库“变回 SQLite” | `.env` 的 `DB_CONNECTION/DB_DSN` 覆盖了 YAML；保留安装向导追加的末尾覆盖块 |
| Docker 构建慢/失败 | 检查能否访问 Docker Hub；必要时配置镜像加速 |
| 容器 unhealthy | `docker compose logs --tail=100 goforum`；检查 `/data` 属主 |
| 上传 404 | 确认 `STORAGE_PATH` 与卷挂载一致，反代未拦截 `/uploads` |
| 登录后立即掉线 | `APP_URL` 与实际访问协议/域名不一致，Secure Cookie 未发送 |

## 6. 验收清单

1. `sudo ./scripts/deploy.sh --url https://<domain>` 一次执行后 `/healthz` 返回 200。
2. `systemctl status goforum` 为 `active (running)`，重启服务器后自动拉起。
3. `/install` 可完成安装，超管可登录并绑定 TOTP。
4. `sudo goforum backup` 生成加密备份；`GF_BACKUP_KEY` 可解密恢复。
5. `./scripts/docker-deploy.sh` 在空机器上构建并启动，容器为 `healthy`。
6. `docker compose down && docker compose up -d` 后数据仍在。
7. 反代下首页、登录、发帖、上传、SSE 通知正常。
8. `.env` / `goforum.env` 权限为 0600，未进入 Git。
