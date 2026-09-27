#!/bin/sh
# Build the bot here and install it on the SwitchNet host over ssh.
#   deploy/deploy.sh [ssh-host]      (default: switchnet)
# First install (config.yaml, /etc/switchnet-discord.env, the unit) is in
# README.md; this only replaces the binary and restarts it.
set -eu
host="${1:-switchnet}"
arch="$(ssh "$host" uname -m)"
case "$arch" in
  x86_64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$(dirname "$0")/.."
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$tmp/switchnet-discord" .
scp -q "$tmp/switchnet-discord" "$host:/tmp/switchnet-discord"
ssh "$host" 'sudo install -m 755 /tmp/switchnet-discord /opt/switchnet-discord/switchnet-discord &&
  rm /tmp/switchnet-discord && sudo systemctl restart switchnet-discord &&
  sleep 3 && systemctl is-active switchnet-discord'
