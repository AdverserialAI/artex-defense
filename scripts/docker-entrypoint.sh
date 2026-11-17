#!/bin/sh
set -eu

export HOME=/home/artex

# Started as root (self-hosting): make the data volume and skills writable by the runtime
# user, then drop root for good. ARTEX itself never runs as root.
if [ "$(id -u)" = "0" ]; then
  mkdir -p /app/data /home/artex
  chown -R artex:artex /app/data /home/artex
  if [ -d /app/skills ]; then
    chown -R artex:artex /app/skills
  fi
  exec runuser -u artex -- /app/start.sh "$@"
fi

# Started as a non-root user (hosted, confidential deployments set one explicitly and drop
# every capability): there is nothing to fix and no privilege to drop.
exec /app/start.sh "$@"
