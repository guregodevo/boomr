# memdoor: the release MEMDOOR_RELEASE from GitHub, its SHA256SUMS checked.
echo "== memdoor $MEMDOOR_RELEASE"
have=$(/root/.local/bin/memdoor version 2>/dev/null | awk 'NR==1{print $2}' || true)
if [ "${have#v}" != "${MEMDOOR_RELEASE#v}" ]; then
  curl -fsSL https://memdoor.ai/install.sh | MEMDOOR_INSTALL_SOURCE=github MEMDOOR_GITHUB_RELEASE="$MEMDOOR_RELEASE" bash
fi
/root/.local/bin/memdoor version
echo "memdoor $MEMDOOR_RELEASE" >> /root/boostr-manifest.new
