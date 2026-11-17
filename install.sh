#!/usr/bin/env bash
# ARTEX 安装脚本：① 全部 Docker  ② 本地编译运行
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── docker 环境检测 / 自动安装 ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "Detected Docker and Docker Compose"; return
  fi
  warn "Docker / Docker Compose not detected"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask 'Install Docker automatically? (y/n)' y)" = y ]; then
        if command -v apt-get >/dev/null 2>&1; then
          sudo apt-get update
          sudo apt-get install -y docker.io docker-compose-v2
        else
          die "Install Docker and Compose using the official instructions:https://docs.docker.com/engine/install/"
        fi
        sudo usermod -aG docker "$USER" || true
        ok "Docker installation complete (log in again after group changes to run without sudo)"
      else
        die "Install Docker manually and retry"
      fi ;;
    Darwin) die "On macOS, install Docker Desktop:https://www.docker.com/products/docker-desktop/" ;;
    *)      die "Install Docker manually and retry" ;;
  esac
}

# ── ① 全部 Docker ───────────────────────────────
install_docker(){
  ensure_docker
  # 凭据、Compose 校验、健康检查和失败诊断统一由 deploy.sh 处理，
  # 避免交互安装与常规更新使用不同的安全默认值。
  bash ./deploy.sh
  ok "Docker installation complete"
}

# ── ② 本地编译运行 ──────────────────────────────
install_local(){
  echo "Database setup:"
  echo "  1) Connect to existing PostgreSQL"
  echo "  2) Start PostgreSQL with Docker (requires Docker)"
  case "$(ask 'Select' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres password (Enter for random)' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Database host' 127.0.0.1)"
      DB_PORT="$(ask 'Port' 5432)"
      DB_USER="$(ask 'Username' artex)"
      DB_PASS="$(ask 'Password' '')"
      DB_NAME="$(ask 'Database name' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # 对 Shell 无法安全表示的控制字符直接报错，其余引号/反斜杠按 JSON 转义。
  json_value(){
    local value="$1"
    [[ ! "$value" =~ [[:cntrl:]] ]] || die "Database configuration must not contain control characters"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    printf '%s' "$value"
  }
  [[ "$DB_PORT" =~ ^[0-9]+$ ]] && [ "$DB_PORT" -ge 1 ] && [ "$DB_PORT" -le 65535 ] || die "Database port must be a number from 1 to 65535"
  case "$DB_SSL" in disable|require|verify-ca|verify-full) ;; *) die "Unsupported sslmode:$DB_SSL" ;; esac
  DB_HOST_JSON="$(json_value "$DB_HOST")"
  DB_USER_JSON="$(json_value "$DB_USER")"
  DB_PASS_JSON="$(json_value "$DB_PASS")"
  DB_NAME_JSON="$(json_value "$DB_NAME")"
  umask 077
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST_JSON}",
    "port": ${DB_PORT},
    "user": "${DB_USER_JSON}",
    "password": "${DB_PASS_JSON}",
    "dbname": "${DB_NAME_JSON}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  chmod 600 config.json
  ok "Generated config.json (0600)"

  # go 环境检查
  command -v go >/dev/null 2>&1 || die "Go was not detected. Install Go >=1.26 first:https://go.dev/dl/"
  ok "Go: $(go version)"

  # 内嵌前端需要 node 出静态产物
  if command -v npm >/dev/null 2>&1; then
    . ./scripts/lib-build.sh
    build_embedded "Building static frontend…" "Compiling binary with embedded frontend…"
  else
    warn "npm not detected: compiling backend without embedded frontend (run npm run dev separately for the frontend)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Build complete → ./artex"

  info "Starting… (Ctrl-C to exit)"
  ./artex
}

# ── ③ 服务器一键部署（二进制包 + 本机 PostgreSQL）────────────────
# 非交互（幂等，可重复执行=升级）：
#   sudo ./install.sh --server [artex-xxx.tar.gz]
# 包省略时自动取当前目录最新的 artex*.tar.gz。重复执行保留 config.json/data/数据库。
# 注意：本脚本【不创建、不管理任何系统服务】（无 systemd unit、无 enable、无自启）。
# 无论全新部署还是覆盖升级都只装文件；启动/停止/重启由用户自行负责
# （start.sh 守护 / 自建 systemd 等），脚本不再代停代起 artex.service。
install_server(){
  local pkg="${1:-}"
  [ "$(id -u)" = "0" ] || die "Server installation requires root:sudo ./install.sh --server"
  command -v apt-get >/dev/null 2>&1 || die "This mode supports apt-based systems (Ubuntu/Debian) only. Follow the deployment manual for other distributions"
  command -v systemctl >/dev/null 2>&1 || die "systemd was not detected"

  # 1. 定位部署包
  if [ -z "$pkg" ]; then
    pkg="$(find . -maxdepth 1 -type f -name 'artex*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -nr | sed -n '1s/^[^ ]* //p')"
    [ -n "$pkg" ] || die "No artex*.tar.gz deployment package in the current directory. Copy it here or specify its path:./install.sh --server /path/artex-xxx.tar.gz"
  fi
  [ -f "$pkg" ] || die "Deployment package does not exist:$pkg"
  info "Deployment package:$pkg"

  # 2. 系统依赖
  info "Installing system dependencies…"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    postgresql postgresql-client curl ca-certificates tar gzip python3 logrotate

  # 3. PostgreSQL is ready（只本次启动，不 enable 开机自启；包管理器默认行为不受本脚本控制）
  systemctl start postgresql
  local i; for i in $(seq 1 30); do pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 && break; sleep 1; done
  pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 || die "PostgreSQL is not ready"
  ok "PostgreSQL is ready"

  # 4. 库与Username（幂等；Password复用已有 config.json，否则随机生成，不落屏）
  local ARTEX_DIR=/opt/artex db_pass="" db_pass_sql=""
  if [ -f "$ARTEX_DIR/config.json" ]; then
    if ! db_pass="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["database"]["password"])' "$ARTEX_DIR/config.json")" || [ -z "$db_pass" ]; then
      die "Cannot read database.password from $ARTEX_DIR/config.json; aborted. The database password will never be reset after a configuration read failure"
    fi
  else
    db_pass="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
  fi
  # Password只经环境变量交给 Python，并以 SQL literal 通过 stdin 执行；不出现在 psql argv/历史中。
  db_pass_sql="$(DB_PASS="$db_pass" python3 -c 'import os; p=os.environ["DB_PASS"]; assert "\0" not in p; print("\x27" + p.replace("\x27", "\x27\x27") + "\x27")')"
  runuser -u postgres -- psql -v ON_ERROR_STOP=1 -q <<SQL
DO \$do\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'artex') THEN
    CREATE ROLE artex LOGIN;
  END IF;
END
\$do\$;
ALTER ROLE artex WITH LOGIN PASSWORD ${db_pass_sql};
SQL
  runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='artex'" | grep -q 1 \
    || runuser -u postgres -- createdb -O artex artex
  ok "Database artex / user artex ready (password hidden; see $ARTEX_DIR/config.json)"

  # 更新前先做一次独立数据库备份；Password只通过环境变量传给 pg_dump。
  local backup_dir="/opt/artex-backups/$(date +%Y%m%d-%H%M%S)"
  install -d -m 700 "$backup_dir"
  if runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='artex'" | grep -q 1; then
    PGPASSWORD="$db_pass" pg_dump -h 127.0.0.1 -p 5432 -U artex -d artex -Fc -f "$backup_dir/artex.dump"
    chmod 600 "$backup_dir/artex.dump"
    ok "Database backed up to $backup_dir/artex.dump"
  fi

  # 5. 解包安装（保留 data/config；二进制先备份，备份只留最近 5 份）
  install -d -m 755 "$ARTEX_DIR" "$ARTEX_DIR/data"
  chmod 700 "$ARTEX_DIR/data"
  # 不做任何服务管理：不检测/停止/重启 artex.service。若旧二进制仍在运行，
  # 先由用户自行停止（或任由下面 rm + 解包替换；Linux 下解包前删除旧文件可
  # 避免覆盖正在运行的可执行文件报 ETXTBSY）。
  local tmp src backup="" skills_backup="" installed=0 completed=0
  tmp="$(mktemp -d)"
  rollback_install(){
    local status=$?
    trap - EXIT
    if [ "$completed" -ne 1 ]; then
      warn "Installation failed (status $status); restoring previous deployment state…"
      if [ "$installed" -eq 1 ]; then
        if [ -n "$backup" ] && [ -f "$backup" ]; then
          cp -a "$backup" "$ARTEX_DIR/artex"
        else
          rm -f "$ARTEX_DIR/artex"
        fi
        if [ -n "$skills_backup" ] && [ -d "$skills_backup" ]; then
          rm -rf "$ARTEX_DIR/skills"
          cp -a "$skills_backup" "$ARTEX_DIR/skills"
        fi
      fi
    fi
    rm -rf "$tmp"
    exit "$status"
  }
  trap rollback_install EXIT

  tar -xzf "$pkg" -C "$tmp"
  # 兼容「包内有单层目录」与「平铺」两种打包方式。
  src="$tmp"
  if [ "$(ls -A "$tmp" | wc -l)" = "1" ] && [ -d "$tmp"/"$(ls "$tmp")" ]; then src="$tmp/$(ls "$tmp")"; fi
  [ -f "$src/artex" ] || die "artex binary missing from package; invalid package structure"
  [ "$(head -c 4 "$src/artex")" = "$(printf '\177ELF')" ] || die "Packaged artex is not an ELF executable. Build a Linux binary with embedded frontend first"

  if [ -f "$ARTEX_DIR/artex" ]; then
    backup="$ARTEX_DIR/artex.bak-$(date +%Y%m%d-%H%M%S)"
    cp -a "$ARTEX_DIR/artex" "$backup"
  fi
  if [ -d "$ARTEX_DIR/skills" ]; then
    skills_backup="$tmp/skills.before"
    cp -a "$ARTEX_DIR/skills" "$skills_backup"
  fi

  # data/ 和 config.json 永远不属于程序包覆盖范围；从此时起失败会触发完整回滚。
  installed=1
  # 先删旧二进制再解包：覆盖正在运行的可执行文件会 ETXTBSY 导致安装失败；
  # unlink 后运行中的进程仍持有旧 inode，新文件照常落地（已备份，失败可回滚）。
  rm -f "$ARTEX_DIR/artex"
  (cd "$src" && tar --exclude='./data' --exclude='./config.json' -cf - .) | tar -xf - -C "$ARTEX_DIR"
  chmod +x "$ARTEX_DIR/artex"
  for executable in start.sh install.sh; do
    [ ! -f "$ARTEX_DIR/$executable" ] || chmod +x "$ARTEX_DIR/$executable"
  done
  # 备份只留最近 5 份；find 无匹配也返回成功，不会让 pipefail 误中断安装。
  find "$ARTEX_DIR" -maxdepth 1 -type f -name 'artex.bak-*' -printf '%T@ %p\n' \
    | sort -nr | sed -n '6,$s/^[^ ]* //p' | while IFS= read -r old; do
      [ -z "$old" ] || rm -f -- "$old"
    done
  ok "Program installed at $ARTEX_DIR"

  # 5.1 日志轮转：start.sh / nohup 都是 append 直写 artex.log，不配轮转会无限增长
  # （debug 级别下尤甚）。copytruncate 适配 append 模式，无需通知进程。
  cat > /etc/logrotate.d/artex <<'LR'
/opt/artex/artex.log {
    daily
    rotate 7
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
}
LR

  # 6. config.json（已存在则不动，避免覆盖现场配置）
  if [ ! -f "$ARTEX_DIR/config.json" ]; then
    cat > "$ARTEX_DIR/config.json" <<JSON
{
  "database": {
    "host": "127.0.0.1",
    "port": 5432,
    "user": "artex",
    "password": "$db_pass",
    "dbname": "artex",
    "sslmode": "disable"
  },
  "skill_dir": "$ARTEX_DIR/skills"
}
JSON
    chmod 600 "$ARTEX_DIR/config.json"
    ok "Generated config.json (0600)"
  else
    info "Using existing config.json"
  fi

  # 7. 无服务处理：不创建/启用/重启任何系统服务。升级场景下旧进程仍在跑旧版本，
  #    由用户自行重启其运行方式（start.sh 守护 / 自建 systemd / supervisor）生效。

  ok "Deployment complete"
  echo "    Program directory: $ARTEX_DIR (configuration $ARTEX_DIR/config.json, 0600)"
  echo "    Log: $ARTEX_DIR/artex.log (logrotate: daily, keep 7)"
  echo "    Start: this script does not create/manage services. Run manually:"
  echo "                cd $ARTEX_DIR && nohup ./start.sh >> $ARTEX_DIR/artex.log 2>&1 &"
  echo "              For an upgrade, stop the old process before starting the new version"
  echo "              For startup at boot, create your own systemd unit; this script does not create one"
  echo "    Initial setup: open /setup to set the administrator password; configure LLM in System → LLM"
  completed=1
  trap - EXIT
  rm -rf "$tmp"
}

echo "=============================="
echo "  artex installation"
echo "  1) Full Docker installation"
echo "  2) Run locally (build with Go)"
echo "  3) Server installation (binary package + PostgreSQL; no autostart service created)"
echo "=============================="
case "${1:-}" in
  --server) shift; install_server "$@" ;;  # 非交互直连
  "") case "$(ask 'Select' 3)" in
    1) install_docker ;;
    2) install_local ;;
    3) install_server ;;
    *) die "Invalid selection" ;;
  esac ;;
  *) die "Unknown argument:$1(supports --server [package-path])" ;;
esac
