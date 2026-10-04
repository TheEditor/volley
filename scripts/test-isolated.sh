#!/usr/bin/env bash
# Run development Go checks with disposable identity, state and temp roots.
set -euo pipefail
# A build target is explicit fixture input, not a public Volley setting.
target_env=()
if [[ ${1:-} == --target=* ]]; then
  target=${1#--target=}
  case "$target" in
    linux/arm64|linux/amd64|darwin/arm64|darwin/amd64) ;;
    *) echo "Unsupported test target: $target" >&2; exit 1 ;;
  esac
  target_env=("GOOS=${target%/*}" "GOARCH=${target#*/}")
  shift
fi
root=$(mktemp -d "${TMPDIR:-/tmp}/volley-go-check.XXXXXX")
trap 'rm -rf "$root"' EXIT
mkdir -p "$root"/{home,config,state,data,cache,runtime,tmp,tmux,tools}
chmod 700 "$root/runtime"
go_path=$(command -v go)
go_cache=$(go env GOCACHE)
module_cache=$(go env GOMODCACHE)
ln -s "$go_path" "$root/tools/go"
env -i HOME="$root/home" PATH="$root/tools:/usr/bin:/bin:/usr/sbin:/sbin" \
  XDG_CONFIG_HOME="$root/config" XDG_STATE_HOME="$root/state" \
  XDG_DATA_HOME="$root/data" XDG_CACHE_HOME="$root/cache" XDG_RUNTIME_DIR="$root/runtime" \
  TMPDIR="$root/tmp" TMUX_TMPDIR="$root/tmux" \
  GOCACHE="$go_cache" GOMODCACHE="$module_cache" GOTOOLCHAIN=local \
  TERM=dumb LC_ALL=C "${target_env[@]}" go "$@"
