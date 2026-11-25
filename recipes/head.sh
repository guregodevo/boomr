#!/bin/bash
# Written by boostr (up --with, deploy): installs the versions pinned below
# and says how it went in /root/boostr-ready, /root/boostr-failed and
# /root/boostr-setup.log. What is already at its pinned version is kept,
# what differs is upgraded (or rolled back): run again, it changes nothing.
# boostr deploy saves a newer script as /root/boostr-setup.sh; a box
# started again runs that one, never an older one.
set -euo pipefail
if [ "${BOOSTR_SETUP:-}" != 1 ] && [ -f /root/boostr-setup.sh ] && ! cmp -s "$0" /root/boostr-setup.sh; then
  BOOSTR_SETUP=1 exec bash /root/boostr-setup.sh
fi
[ -f /root/boostr-setup.sh ] || ! [ -f "$0" ] || cp "$0" /root/boostr-setup.sh
rm -f /root/boostr-ready /root/boostr-failed
exec > /root/boostr-setup.log 2>&1
trap 'echo "failed at line $LINENO: $BASH_COMMAND"; touch /root/boostr-failed' ERR
export DEBIAN_FRONTEND=noninteractive HOME=/root PATH=/usr/local/go/bin:/usr/local/cuda/bin:/root/.local/bin:$PATH
: > /root/boostr-manifest.new

echo "== packages"
APT="git cmake build-essential curl ca-certificates python3"
missing=$(for p in $APT; do dpkg -s "$p" >/dev/null 2>&1 || echo "$p"; done)
[ -z "$missing" ] || { apt-get -qq update && apt-get -qq install -y $missing; }
git config --global --add safe.directory '*'
