# janis: Go, janis built with ggml's CUDA kernels at JANIS_COMMIT, MODEL at
# MODEL_REV.
echo "== go"
GO=go1.25.0
go version 2>/dev/null | grep -q "$GO " || {
  curl -fsSL "https://go.dev/dl/$GO.linux-amd64.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
}

echo "== janis $JANIS_REF at $JANIS_COMMIT"
[ -d /root/janis ] || git clone -q https://github.com/guregodevo/janis.git /root/janis
cd /root/janis
git cat-file -e "$JANIS_COMMIT^{commit}" 2>/dev/null || git fetch -q origin
git checkout -q --detach "$JANIS_COMMIT"
if [ "$(cat /root/.janis-built 2>/dev/null)" != "$JANIS_COMMIT" ] || ! [ -x /usr/local/bin/janis ]; then
  bash scripts/fetch-ggml.sh --cuda
  make tokenizer-lib
  go build -tags ggml -o /usr/local/bin/janis ./cmd/janis
  echo "$JANIS_COMMIT" > /root/.janis-built
fi
cd /root
echo "janis $JANIS_COMMIT ($JANIS_REF)" >> /root/boostr-manifest.new

echo "== $MODEL at $MODEL_REV"
# One connection reads 3-5 MB/s from some vast hosts: each file in 8
# ranges. A file already there at its full size is not read again; the
# model's other revisions are removed once this one is whole (janis loads
# whichever snapshot it finds).
REPO=mlx-community/$MODEL
repo=/root/.cache/huggingface/hub/models--${REPO/\//--}
snap=$repo/snapshots/$MODEL_REV
mkdir -p "$snap" "$repo/refs"
for f in $(curl -fsSL "https://huggingface.co/api/models/$REPO/revision/$MODEL_REV" | python3 -c 'import json,sys; print(" ".join(s["rfilename"] for s in json.load(sys.stdin)["siblings"]))'); do
  u="https://huggingface.co/$REPO/resolve/$MODEL_REV/$f"
  n=$(curl -sIL "$u" | grep -i '^content-length' | tail -1 | tr -dc 0-9)
  [ -f "$snap/$f" ] && [ "$(stat -c %s "$snap/$f")" = "$n" ] && continue
  mkdir -p "$(dirname "$snap/$f")"; c=$(( (n + 7) / 8 ))
  for i in 0 1 2 3 4 5 6 7; do
    s=$((i*c)); e=$((s+c-1)); [ $e -ge $n ] && e=$((n-1))
    curl -sL --retry 5 -r $s-$e -o "$snap/$f.$i" "$u" &
  done
  wait
  cat "$snap/$f".[0-7] > "$snap/$f.tmp" && rm -f "$snap/$f".[0-7]
  [ "$(stat -c %s "$snap/$f.tmp")" = "$n" ] || { echo "$f: $(stat -c %s "$snap/$f.tmp") bytes of $n"; false; }
  mv "$snap/$f.tmp" "$snap/$f" && echo "$f $n"
done
for old in "$repo"/snapshots/*; do [ "$old" = "$snap" ] || rm -rf "$old"; done
echo "$MODEL_REV" > "$repo/refs/main"
echo "model $MODEL $MODEL_REV" >> /root/boostr-manifest.new
