# syntax=docker/dockerfile:1
#
# 运行镜像（不在镜像里编译）：只装常用工具，放入**预编译好的 Linux 单二进制**。
# 二进制由 CI 的 binaries job 交叉编译（纯 Go、无 QEMU），按目标架构放在
# 构建上下文的 dist/artex-linux-<TARGETARCH>/artex（与 ./build.sh 单目标输出一致）。
# 这样多架构构建时 arm64 只需模拟 apt 层，不再模拟 Next/Go 编译，速度快得多。
#
# 本地手动构建镜像时，先自行准备二进制：
#   ARTEX_TARGET_OS=linux ARTEX_TARGET_ARCH=amd64 ./build.sh
#   docker build -t artex:local .
# 本地 docker compose 无需准备 dist：会自动使用 Dockerfile.source 从源码构建。
FROM node:22-bookworm@sha256:e13726ca604184d7a91fcacab1fc09eefd642dc28e79ac34e85540dd93b78cfb AS node-runtime
FROM python:3.12-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df
ARG TARGETARCH
# 65532 is the fixed non-root user hosted deployments start this image as; the entrypoint
# also drops to it when the container is started as root (self-hosting).
ARG ARTEX_UID=65532 ARTEX_GID=65532
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright
RUN groupadd --gid ${ARTEX_GID} artex \
    && useradd --uid ${ARTEX_UID} --gid ${ARTEX_GID} --home-dir /home/artex --create-home --shell /usr/sbin/nologin artex
# Node is copied from the pinned official image instead of executing a remote setup script.
COPY --from=node-runtime /usr/local/bin/ /usr/local/bin/
COPY --from=node-runtime /usr/local/lib/node_modules/ /usr/local/lib/node_modules/
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && rm -rf /var/lib/apt/lists/*
# 预装 Playwright MCP 与 CLI（全局），运行时不再 npx 联网下载。
# @playwright/mcp：browser MCP 直接 `npx @playwright/mcp`（已全局装好，无需 -y/@latest）。
# @playwright/cli：提供 playwright-cli，装完顺带 --help 验证可执行。
# 再装 playwright（提供浏览器管理），装完用 --with-deps 预置 chromium 及其系统依赖，
# 这样容器内 MCP/CLI 首次启动即可用，不再联网下载浏览器。
RUN npm install -g @playwright/mcp@0.0.85 @playwright/cli@0.1.24 playwright@1.65.1 \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 预编译好的对应架构二进制（与 build.sh 的 dist/artex-linux-<arch>/artex 对应）
COPY dist/artex-linux-${TARGETARCH}/artex /app/artex
# 守护启动脚本：转发停止信号，并在进程异常退出时按退出码决定是否重新拉起。
# 它同时负责把 SIGTERM 转发给 artex —— docker stop 只把信号发给 PID 1，
# 不转发的话 artex 收不到、做不了优雅关闭，10 秒后被 SIGKILL 硬杀。
COPY start.sh /app/start.sh
# start.sh 启动时即 source 配置迁移函数；upgrade-prompts 也依赖同目录 SQL。
COPY scripts/artex-config.sh scripts/upgrade-review-flow-prompts.sql /app/scripts/
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
# JWT 签名密钥落到持久化数据卷，容器重建后旧登录 token 仍有效。
RUN ln -sf /app/data/jwt.key /app/jwt.key \
    && chmod +x /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /app/data /home/artex \
    && chown -R ${ARTEX_UID}:${ARTEX_GID} /app/data /app/skills /home/artex \
    && chmod -R a+rX /app /opt/playwright
ENV HOME=/home/artex
# data/（SQLite + jwt.key）持久化点
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
