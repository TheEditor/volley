#!/usr/bin/env bash
# Run development Go checks with disposable identity, state and temp roots.
set -euo pipefail
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
  TERM=dumb LC_ALL=C go "$@"
