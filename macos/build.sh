#!/bin/sh
# Builds Lumen.app (the Lumen window as a macOS app) and installs it in
# ~/Applications. The app runs the `lumen` binary of this checkout, so build
# that first (go build -trimpath -o bin/lumen ./cmd/lumen).
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
dest=${1:-"$HOME/Applications/Lumen.app"}
[ -x "$root/bin/lumen" ] || { echo "build bin/lumen first" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/Lumen.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

swiftc -O -o "$app/Contents/MacOS/Lumen" macos/Lumen.swift -framework AppKit -framework WebKit
sed "s|__LUMEN_BINARY__|$root/bin/lumen|" macos/Info.plist > "$app/Contents/Info.plist"

# Icon: every size of the iconset from one 1024 px drawing.
set=$work/AppIcon.iconset
mkdir -p "$set"
go run macos/icon.go "$work/icon.png"
for s in 16 32 128 256 512; do
  sips -z $s $s "$work/icon.png" --out "$set/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2))
  sips -z $d $d "$work/icon.png" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$set" -o "$app/Contents/Resources/AppIcon.icns"

codesign --force --sign - "$app" >/dev/null 2>&1
mkdir -p "$(dirname "$dest")"
rm -rf "$dest"
mv "$app" "$dest"
# Refresh Launch Services so Spotlight and the Dock pick up the new app.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$dest" >/dev/null 2>&1 || true
echo "installed $dest"
