#!/bin/sh
# 打成上传 Chrome 应用商店的 zip：去掉 manifest 里开发用的 key（商店给自己的 id）
set -e
cd "$(dirname "$0")/.."
version=$(python3 -c 'import json;print(json.load(open("extension/manifest.json"))["version"])')
tmp=$(mktemp -d)
cp -R extension/. "$tmp/"
python3 - "$tmp/manifest.json" <<'PY'
import json, sys
p = sys.argv[1]
m = json.load(open(p))
m.pop("key", None)
json.dump(m, open(p, "w"), indent=2, ensure_ascii=False)
PY
mkdir -p dist
out="$PWD/dist/annulo-extension-$version.zip"
rm -f "$out"
(cd "$tmp" && zip -qr "$out" .)
rm -rf "$tmp"
echo "$out"
