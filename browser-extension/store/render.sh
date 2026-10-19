#!/bin/sh
# 把 shots.html 渲染成商店要的图：out/<语言>-<n>.png（1280×800）、out/<语言>-tile.png（440×280），去掉透明层（商店只收 24 位 PNG）
set -e
cd "$(dirname "$0")"
chrome="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
mkdir -p out
for lang in en zh; do
  for shot in 1 2 3 tile; do
    if [ "$shot" = tile ]; then size=440,280; else size=1280,800; fi
    out="$PWD/out/$lang-$shot.png"
    "$chrome" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 --allow-file-access-from-files \
      --window-size=$size --screenshot="$out" "file://$PWD/shots.html?shot=$shot&lang=$lang" 2>/dev/null
    python3 -c "import sys;from PIL import Image;Image.open(sys.argv[1]).convert('RGB').save(sys.argv[1])" "$out"
  done
done
ls -1 out
