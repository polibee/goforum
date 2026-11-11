#!/usr/bin/env bash
# GoForum bare-metal deployment for systemd Linux.
#
# Builds the frontend and backend, installs the binary under APP_DIR,
# generates APP_KEY / GF_BACKUP_KEY on first run, installs a hardened
# systemd unit and waits for /healthz.
#
# Usage:
#   sudo ./scripts/deploy.sh
#   sudo ./scripts/deploy.sh --url https://forum.example.com
#   sudo ./scripts/deploy.sh --binary ./bin/goforum --upgrade
#
set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${APP_DIR:-/opt/goforum}"
APP_USER="${APP_USER:-goforum}"
SERVICE_NAME="${SERVICE_NAME:-goforum}"
APP_ENV="${APP_ENV:-production}"
HTTP_ADDR="${HTTP_ADDR:-127.0.0.1:8080}"
SITE_URL="${SITE_URL:-}"
VERSION="${VERSION:-}"
BINARY="${BINARY:-}"
INSTALL_SERVICE=1
UPGRADE=0
ADDR_SET=0
URL_SET=0
ENV_SET=0

usage() {
  cat <<'EOF'
GoForum 一键部署（systemd）

用法：
  sudo ./scripts/deploy.sh [选项]

选项：
  --dir PATH        安装目录（默认 /opt/goforum）
  --user NAME       运行用户（默认 goforum）
  --addr HOST:PORT  监听地址（默认 127.0.0.1:8080）
  --url URL         站点公开地址，例如 https://forum.example.com
  --env ENV         APP_ENV，默认 production；本地调试可用 development
  --binary PATH     使用已编译好的二进制，跳过 Node/Go 构建
  --version VER     版本号，默认取 git describe
  --upgrade         重启前执行 goforum migrate（升级时使用）
  --no-service      只安装文件和配置，不创建 systemd 服务
  -h, --help        显示帮助

环境变量：APP_DIR / APP_USER / SERVICE_NAME / HTTP_ADDR / SITE_URL / VERSION / BINARY
EOF
}

log() { printf '\033[1;32m[goforum]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[goforum]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m[goforum]\033[0m %s\n' "$*" >&2; exit 1; }

while (($#)); do
  case "$1" in
    --dir) APP_DIR="${2:?--dir 需要路径}"; shift 2 ;;
    --user) APP_USER="${2:?--user 需要用户名}"; shift 2 ;;
    --addr) HTTP_ADDR="${2:?--addr 需要 HOST:PORT}"; ADDR_SET=1; shift 2 ;;
    --url) SITE_URL="${2:?--url 需要 URL}"; URL_SET=1; shift 2 ;;
    --env) APP_ENV="${2:?--env 需要值}"; ENV_SET=1; shift 2 ;;
    --binary) BINARY="${2:?--binary 需要文件路径}"; shift 2 ;;
    --version) VERSION="${2:?--version 需要值}"; shift 2 ;;
    --upgrade) UPGRADE=1; shift ;;
    --no-service) INSTALL_SERVICE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1（使用 --help 查看帮助）" ;;
  esac
done

[[ "$APP_DIR" != *" "* ]] || die "安装目录不能包含空格"
[[ "$(uname -s)" == "Linux" ]] || die "该脚本仅支持 systemd Linux；Docker 请使用 scripts/docker-deploy.sh"
if [[ -n "$SITE_URL" && "${APP_ENV,,}" == "production" && "${SITE_URL,,}" != https://* ]]; then
  die "生产环境 APP_URL 必须是 https://（站点 Cookie 依赖它设置 Secure）"
fi

if [[ "$INSTALL_SERVICE" == "1" && "$(id -u)" -ne 0 ]]; then
  die "安装 systemd 服务需要 root：sudo $0 $*"
fi

require() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"
}

if [[ -z "$BINARY" ]]; then
  require go
  require npm
fi
require openssl
require install
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  die "需要 curl 或 wget 用于健康检查"
fi
[[ "$INSTALL_SERVICE" == "0" ]] || require systemctl

http_ok() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 2 "$1" >/dev/null 2>&1
  else
    wget -q -T 2 -O /dev/null "$1" >/dev/null 2>&1
  fi
}

if [[ -z "$VERSION" ]]; then
  if git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
  else
    VERSION="dev"
  fi
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

if [[ -n "$BINARY" ]]; then
  [[ -f "$BINARY" ]] || die "二进制不存在：$BINARY"
  install -m 0755 "$BINARY" "$TMP_DIR/goforum"
  log "使用预编译二进制：$BINARY"
else
  log "构建前端资源（npm）..."
  (
    cd "$ROOT_DIR"
    if [[ -f package-lock.json ]]; then
      npm ci --include=dev --no-audit --no-fund
    else
      npm install --include=dev --no-audit --no-fund
    fi
    npm run build
  )
  log "编译 GoForum（$VERSION）..."
  (
    cd "$ROOT_DIR"
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o "$TMP_DIR/goforum" ./cmd/goforum
  )
fi

if [[ "$INSTALL_SERVICE" == "1" ]]; then
  if ! id -u "$APP_USER" >/dev/null 2>&1; then
    log "创建系统用户：$APP_USER"
    if command -v useradd >/dev/null 2>&1; then
      useradd --system --home-dir "$APP_DIR" --shell /usr/sbin/nologin "$APP_USER"
    else
      adduser -S -H -h "$APP_DIR" -s /sbin/nologin "$APP_USER"
    fi
  fi
fi

log "创建目录：$APP_DIR"
install -d -m 0755 -o root -g root "$APP_DIR"
if [[ "$INSTALL_SERVICE" == "1" ]]; then
  install -d -m 0755 -o "$APP_USER" -g "$APP_USER" "$APP_DIR/data" "$APP_DIR/data/uploads" "$APP_DIR/backups"
  install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$APP_DIR/configs"
else
  install -d -m 0755 "$APP_DIR/data" "$APP_DIR/data/uploads" "$APP_DIR/backups" "$APP_DIR/configs"
fi

if [[ "$INSTALL_SERVICE" == "1" ]] && systemctl is-active --quiet "$SERVICE_NAME"; then
  log "停止现有服务：$SERVICE_NAME"
  systemctl stop "$SERVICE_NAME"
fi

install -m 0755 -o root -g root "$TMP_DIR/goforum" "$APP_DIR/goforum"

ENV_FILE="$APP_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  log "生成 $ENV_FILE（APP_KEY 与备份密钥）"
  APP_KEY="$(openssl rand -base64 32)"
  BACKUP_KEY="$(openssl rand -base64 32)"
  {
    echo "# Generated by scripts/deploy.sh on $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "# Keep this file private. Back up APP_KEY and GF_BACKUP_KEY offline."
    echo "APP_ENV=$APP_ENV"
    [[ -n "$SITE_URL" ]] && echo "APP_URL=$SITE_URL"
    echo "APP_KEY=$APP_KEY"
    echo "HTTP_ADDR=$HTTP_ADDR"
    echo "DB_CONNECTION=sqlite"
    echo "DB_DATABASE=./data/goforum.db"
    echo "CACHE_STORE=memory"
    echo "FILESYSTEM_DISK=local"
    echo "STORAGE_PATH=./data/uploads"
    echo "LOG_LEVEL=info"
    echo "LOG_CHANNEL=json"
    echo "SESSION_COOKIE=gf_session"
    echo "WORKER_ENABLED=true"
    echo "GF_BACKUP_KEY=$BACKUP_KEY"
  } >"$ENV_FILE"
  if [[ "$INSTALL_SERVICE" == "1" ]]; then
    chown "$APP_USER:$APP_USER" "$ENV_FILE"
  fi
  chmod 0600 "$ENV_FILE"
  GENERATED_SECRETS=1
else
  log "保留已有配置：$ENV_FILE"
  grep -q '^APP_KEY=.\+' "$ENV_FILE" || die "$ENV_FILE 缺少 APP_KEY"
  if [[ "$ADDR_SET" == "1" ]]; then
    sed -i "s|^HTTP_ADDR=.*|HTTP_ADDR=$HTTP_ADDR|" "$ENV_FILE"
  fi
  if [[ "$ENV_SET" == "1" ]]; then
    sed -i "s|^APP_ENV=.*|APP_ENV=$APP_ENV|" "$ENV_FILE"
  fi
  if [[ "$URL_SET" == "1" ]]; then
    if grep -q '^APP_URL=' "$ENV_FILE"; then
      sed -i "s|^APP_URL=.*|APP_URL=$SITE_URL|" "$ENV_FILE"
    else
      printf 'APP_URL=%s\n' "$SITE_URL" >>"$ENV_FILE"
    fi
  fi
  GENERATED_SECRETS=0
fi

if [[ "$INSTALL_SERVICE" == "1" ]]; then
  chown "$APP_USER:$APP_USER" "$ENV_FILE"
fi
chmod 0600 "$ENV_FILE"

if [[ "$INSTALL_SERVICE" == "1" ]]; then
  log "写入 systemd 服务：/etc/systemd/system/$SERVICE_NAME.service"
  cat >"/etc/systemd/system/$SERVICE_NAME.service" <<EOF
[Unit]
Description=GoForum community forum
Documentation=https://github.com/polibee/goforum
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$APP_USER
Group=$APP_USER
WorkingDirectory=$APP_DIR
Environment=GF_ENV_FILE=$ENV_FILE
Environment=GF_CONFIG=$APP_DIR/configs/goforum.yaml
EnvironmentFile=-$ENV_FILE
ExecStart=$APP_DIR/goforum
Restart=on-failure
RestartSec=5s
TimeoutStopSec=20s
LimitNOFILE=65535
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=$APP_DIR/data $APP_DIR/backups $APP_DIR/configs

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "/etc/systemd/system/$SERVICE_NAME.service"
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME" >/dev/null

  log "写入管理命令：/usr/local/bin/goforum"
  cat >/usr/local/bin/goforum <<EOF
#!/usr/bin/env bash
set -euo pipefail
if [ "\$(id -u)" -eq 0 ]; then
  exec runuser -u "$APP_USER" -- "\$0" "\$@"
fi
cd "$APP_DIR"
set -a
. "$APP_DIR/.env"
set +a
export GF_ENV_FILE="$APP_DIR/.env"
export GF_CONFIG="$APP_DIR/configs/goforum.yaml"
exec "$APP_DIR/goforum" "\$@"
EOF
  chmod 0755 /usr/local/bin/goforum

  if [[ "$UPGRADE" == "1" ]]; then
    log "执行数据库迁移..."
    runuser -u "$APP_USER" -- env \
      GF_ENV_FILE="$ENV_FILE" \
      GF_CONFIG="$APP_DIR/configs/goforum.yaml" \
      bash -c 'cd "$1" && exec "$2" migrate' _ "$APP_DIR" "$APP_DIR/goforum"
  fi

  log "启动服务：$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"

  PORT="${HTTP_ADDR##*:}"
  HEALTH_URL="http://127.0.0.1:${PORT}/healthz"
  HEALTHY=0
  for _ in $(seq 1 30); do
    if http_ok "$HEALTH_URL"; then
      HEALTHY=1
      break
    fi
    if ! systemctl is-active --quiet "$SERVICE_NAME"; then
      break
    fi
    sleep 1
  done
  if [[ "$HEALTHY" != "1" ]]; then
    warn "健康检查未通过，最近日志："
    journalctl -u "$SERVICE_NAME" -n 30 --no-pager >&2 || true
    die "服务启动失败，请根据日志排查"
  fi
fi

cat <<EOF

GoForum 部署完成。

  安装目录 : $APP_DIR
  配置文件 : $ENV_FILE
  运行用户 : $APP_USER
  监听地址 : $HTTP_ADDR
  版本     : $VERSION

下一步：
  1. 打开安装向导：http://127.0.0.1:${HTTP_ADDR##*:}/install
     远程服务器请先配置 HTTPS 反向代理，或使用 SSH 隧道：
       ssh -L 8080:127.0.0.1:8080 root@<server>
  2. 安装向导中填写站点名称、公开 URL、管理员账号。
  3. 超管登录后绑定 TOTP，并保存恢复码。

运维命令：
  systemctl status $SERVICE_NAME
  journalctl -u $SERVICE_NAME -f
  sudo goforum backup
  sudo goforum migrate
  升级：git pull && sudo ./scripts/deploy.sh --upgrade

EOF

if [[ "$GENERATED_SECRETS" == "1" ]]; then
  warn "本次生成了 APP_KEY 与 GF_BACKUP_KEY，已写入 $ENV_FILE。"
  warn "请立即把这两个值复制到密码管理器并离线保存；丢失 APP_KEY 会导致已加密设置与 TOTP 无法解密。"
fi
