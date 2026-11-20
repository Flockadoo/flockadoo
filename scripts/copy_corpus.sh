#!/usr/bin/env bash
# Copies flockadoo-server's stream corpus (real engine output, recorded with the
# node's scripts/record_sse.sh) into the client's test data, with a SHA256SUMS
# file, so both repositories test the same bytes (plan section 17.1). Never
# edit a recording here or there; re-record in the node and copy again.
#
#   scripts/copy_corpus.sh ../flockadoo-server
set -euo pipefail
node="${1:?usage: scripts/copy_corpus.sh PATH_TO_FLOCKADOO_NODE}"
src="$node/internal/sse/testdata/corpus"
dst="$(cd "$(dirname "$0")/.." && pwd)/internal/testenv/fakeengine/testdata/corpus"
[ -d "$src" ] || { echo "no corpus at $src" >&2; exit 1; }
rm -rf "$dst"
mkdir -p "$dst"
cp -R "$src"/. "$dst"/
commit="$(git -C "$node" rev-parse HEAD 2>/dev/null || echo unknown)"
cd "$dst"
find . -type f ! -name SHA256SUMS ! -name SOURCE | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS
echo "flockadoo-server $commit internal/sse/testdata/corpus" > SOURCE
echo "copied $(wc -l < SHA256SUMS) files from flockadoo-server $commit"
