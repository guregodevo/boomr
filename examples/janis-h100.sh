#!/bin/bash
# Run at start on a vast.ai machine (boomr up --onstart): Go, janis built
# with ggml's CUDA kernels at JANIS_REF, and the 27B pulled. The log is
# /root/janis-setup.log; /root/janis-ready is written when it is done.
set -euo pipefail
exec > /root/janis-setup.log 2>&1
JANIS_REF=${JANIS_REF:-main}
MODEL=${MODEL:-Qwen3.8-27B-4bit}
export DEBIAN_FRONTEND=noninteractive
apt-get -qq update && apt-get -qq install -y git cmake build-essential curl ca-certificates
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q go1.25; then
  curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:/usr/local/cuda/bin:$PATH
[ -d /root/janis ] || git clone -q https://github.com/guregodevo/janis.git /root/janis
cd /root/janis && git fetch -q && git checkout -q "$JANIS_REF"
bash scripts/fetch-ggml.sh --cuda
make tokenizer-lib
go build -tags ggml -o /usr/local/bin/janis ./cmd/janis
janis version
JANIS_GGML_DEVICE=gpu janis pull "$MODEL"
touch /root/janis-ready
