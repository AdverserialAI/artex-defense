#!/usr/bin/env bash
# ARTEX update脚本：① Docker 从当前源码重建  ② 本地编译更新（重建二进制）
# 与 install.sh 对应：install 负责首次落地，update 负责升级到新版本。
# DB 迁移无需手动执行——artex 每次启动都会幂等重跑 schema.sql（含 ADD COLUMN/CREATE
# INDEX IF NOT EXISTS），所以“重启即迁移”。数据（pgdata 卷、./data、./skills）不受影响。
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 可选：同步仓库到最新代码（compose/脚本/本地编译源码都靠它更新）───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Not a Git checkout; skipping git pull"; return; }
  [ "$(ask 'Pull latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    die "git pull could not fast-forward (local changes or diverging branches); update aborted to avoid using old code"
  fi
}

# ── ① Docker 更新 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "Docker / Docker Compose not detected; deploy with ./install.sh first"
  [ -f .env ] || die ".env not found; run ./install.sh for initial deployment first"

  # 可选：固定本次源码构建产物的镜像 tag；留空则沿用 .env，默认 local。
  local tag; tag="$(ask 'Target image tag (Enter to use .env / local)' '')"
  if [ -n "$tag" ]; then
    [[ "$tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || die "Invalid Docker tag:$tag"
    local env_tmp; env_tmp="$(mktemp)"
    awk -v value="$tag" '
      BEGIN { replaced = 0 }
      /^ARTEX_TAG=/ { if (!replaced) { print "ARTEX_TAG=" value; replaced = 1 }; next }
      { print }
      END { if (!replaced) print "ARTEX_TAG=" value }
    ' .env > "$env_tmp"
    chmod 600 "$env_tmp"
    mv "$env_tmp" .env
    ok "ARTEX_TAG set to ${tag}"
  fi

  # 本仓库 Compose 镜像由 Dockerfile.source 构建，不是可 pull 的上游发布镜像。
  # deploy.sh 同时负责重建、数据库保护、完整健康检查和失败日志诊断。
  bash ./deploy.sh
  ok "Docker update complete"
  info "Clean old images (optional):docker image prune -f"
}

# ── ② 本地编译更新 ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go >=1.26 was not detected:https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found; use ./install.sh for initial deployment"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    . ./scripts/lib-build.sh
    build_embedded "Rebuilding static frontend…" "Recompiling binary with embedded frontend…"
  else
    warn "npm not detected: compiling backend without embedded frontend (run npm run dev separately for the frontend)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Build complete → ./artex"
  warn "Restart the running artex process to apply changes (schema migrates automatically on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (rebuild current source)"
echo "  2) Local update (rebuild with Go)"
echo "=============================="
case "$(ask 'Select' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "Invalid selection" ;;
esac
