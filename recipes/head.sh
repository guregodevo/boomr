#!/bin/bash
# Written by hyperscalr (up --with, deploy): installs the versions pinned below
# and says how it went in /root/hyperscalr-ready, /root/hyperscalr-failed and
# /root/hyperscalr-setup.log. What is already at its pinned version is kept,
# what differs is upgraded (or rolled back): run again, it changes nothing.
# hyperscalr deploy saves a newer script as /root/hyperscalr-setup.sh; a box
# started again runs that one, never an older one.
set -euo pipefail
if [ "${HYPERSCALR_SETUP:-}" != 1 ] && [ -f /root/hyperscalr-setup.sh ] && ! cmp -s "$0" /root/hyperscalr-setup.sh; then
  HYPERSCALR_SETUP=1 exec bash /root/hyperscalr-setup.sh
fi
[ -f /root/hyperscalr-setup.sh ] || ! [ -f "$0" ] || cp "$0" /root/hyperscalr-setup.sh
rm -f /root/hyperscalr-ready /root/hyperscalr-failed
exec > /root/hyperscalr-setup.log 2>&1
trap 'echo "failed at line $LINENO: $BASH_COMMAND"; touch /root/hyperscalr-failed' ERR
export DEBIAN_FRONTEND=noninteractive HOME=/root PATH=/usr/local/go/bin:/usr/local/cuda/bin:/root/.local/bin:$PATH
: > /root/hyperscalr-manifest.new

echo "== packages"
APT="git cmake build-essential curl ca-certificates python3"
missing=$(for p in $APT; do dpkg -s "$p" >/dev/null 2>&1 || echo "$p"; done)
[ -z "$missing" ] || { apt-get -qq update && apt-get -qq install -y $missing; }
git config --global --add safe.directory '*'
