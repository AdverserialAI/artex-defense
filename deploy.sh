#!/usr/bin/env bash
# 一键部署：首次启动自动初始化数据库Password，之后始终复用 .env 与命名卷中的数据。
# 默认构建当前源码，支持 --no-build 严格复用已有镜像，支持 --show-secrets 显式显示凭据。
set -Eeuo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

BUILD=1
SHOW_SECRETS=0
for arg in "$@"; do
  case "$arg" in
    --no-build) BUILD=0 ;;
    --show-secrets) SHOW_SECRETS=1 ;;
    -h|--help)
      echo "Usage: $0 [--no-build] [--show-secrets]"
      echo "  --no-build      Reuse the existing ARTEX_TAG image without rebuilding"
      echo "  --show-secrets  Show database password on success (credentials hidden by default)"
      exit 0
      ;;
    *) die "Unknown argument: $arg" ;;
  esac
done

command -v docker >/dev/null 2>&1 || die "Docker was not found. Install Docker first."
docker compose version >/dev/null 2>&1 || die "Docker Compose plugin was not found."
command -v curl >/dev/null 2>&1 || die "curl was not found."

# 只读取 Compose 使用的键，不 source .env，避免把部署变量当作 Shell 代码执行。
dotenv_value() {
  local key="$1"
  awk -v key="$key" '
    index($0, key "=") == 1 {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
      exit
    }
  ' .env
}

unquote() {
  local value="$1"
  if [[ ${#value} -ge 2 && "$value" == \"*\" && "$value" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ ${#value} -ge 2 && "$value" == \'*\' && "$value" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "$value"
}

validate_existing_env() {
  local pg_password pg_user pg_db
  pg_password="$(unquote "$(dotenv_value POSTGRES_PASSWORD)")"
  pg_user="$(unquote "$(dotenv_value POSTGRES_USER)")"
  pg_db="$(unquote "$(dotenv_value POSTGRES_DB)")"
  pg_user="${pg_user:-artex}"
  pg_db="${pg_db:-artex}"

  case "$pg_password" in
    ""|artex|change-me-please|password|postgres)
      die "POSTGRES_PASSWORD in .env is empty or still a weak default. Set a unique strong password first; the deployment script must not replace the real password of an existing named volume."
      ;;
  esac
  # Compose 将Password直接插入 postgres:// DSN，非 URL 安全字符会改变连接串含义。
  [[ "$pg_password" =~ ^[A-Za-z0-9._~-]+$ ]] || die "Docker POSTGRES_PASSWORD may contain only letters, digits, dots, underscores, tildes, and hyphens to avoid ambiguous DSN/Compose escaping."
  [[ "$pg_user" =~ ^[A-Za-z0-9_-]+$ ]] || die "POSTGRES_USER contains characters unsupported in the Docker DSN."
  [[ "$pg_db" =~ ^[A-Za-z0-9_-]+$ ]] || die "POSTGRES_DB contains characters unsupported in the Docker DSN."
}

if [ ! -f .env ]; then
  info "First deployment: generating .env with a unique random database password"
  command -v openssl >/dev/null 2>&1 || die "openssl was not found; cannot generate a random password."
  pg_pass="$(openssl rand -hex 32)"
  umask 077
  cat > .env <<EOF
# Password仅在首次部署时生成；修改它不会重置已有命名卷的数据库Password。
# Docker DSN 使用 URL 安全字符，避免特殊字符被错误解析。
POSTGRES_PASSWORD=${pg_pass}
POSTGRES_USER=artex
POSTGRES_DB=artex
# 留空即使用最新正式版；指定版本可复现升级/回滚。
ARTEX_TAG=
EOF
  chmod 600 .env
  ok "Generated .env (permissions 600)"
else
  info "Reusing existing .env and database named volume"
  validate_existing_env
  chmod 600 .env
fi

tag="$(unquote "$(dotenv_value ARTEX_TAG)")"
tag="${tag:-local}"
info "Image tag:${tag}"

# 启动前先展开并校验 Compose 配置，避免容器启动后才暴露 DSN/变量错误。
docker compose config --quiet
info "Pulling database base image"
docker compose pull postgres

up_args=(-d)
if [ "$BUILD" -eq 1 ]; then
  info "Building and starting from current source (tag=${tag})"
  up_args+=(--build)
else
  info "Starting with existing image only (tag=${tag}, --no-build)"
  up_args+=(--no-build)
fi

if ! docker compose up "${up_args[@]}"; then
  warn "docker compose up failed; showing recent container logs:"
  docker compose ps >&2 || true
  docker compose logs --no-color --tail 100 postgres artex >&2 || true
  die "Deployment failed. Resolve the errors above and retry."
fi

info "Waiting for backend health and frontend homepage readiness"
healthy=0
for _ in $(seq 1 36); do
  if curl -fsS --max-time 3 http://127.0.0.1:8787/api/health >/dev/null 2>&1 \
    && curl -fsS --max-time 3 http://127.0.0.1:8787/ >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 5
done

if [ "$healthy" -ne 1 ]; then
  warn "Full health check did not pass within 180 seconds; showing container status and recent logs:"
  docker compose ps >&2 || true
  docker compose logs --no-color --tail 150 postgres artex >&2 || true
  die "Health check failed (backend /api/health or frontend /)."
fi

ok "Deployment complete; backend health and frontend homepage checks passed"
echo ""
echo "  URL:  http://<server-IP>:8787/"
echo "  First use: open the page to set up the administrator account, then configure a model in System → LLM."
echo "  Data directory: ./data (JWT key data/jwt.key is managed and persisted automatically)"
echo "  Database: Docker named volume pgdata"
echo ""
if [ "$SHOW_SECRETS" -eq 1 ]; then
  warn "Showing database password as requested by --show-secrets; do not paste this output into public logs."
  printf '  POSTGRES_PASSWORD=%s\n' "$(unquote "$(dotenv_value POSTGRES_PASSWORD)")"
else
  echo "  Database password is stored in .env (permissions 600), hidden by default. Explicitly use --show-secrets to display it."
fi
echo ""
echo "Stop:  docker compose down        (data retained)"
echo "Update: change ARTEX_TAG and rerun ./deploy.sh (rebuilds current source by default)"
