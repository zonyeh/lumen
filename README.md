# Lumen — RAW photo editor for macOS that works with Claude

**Lumen is a free, open-source Lightroom alternative for RAW photos, built to be
edited together with Claude.** It decodes Sony ARW and DNG files itself, develops
them with Lightroom-style controls (white balance, tone, curves, color mixer,
masks, noise reduction, sharpening) and exposes everything to Claude Code through
MCP, so you can say *"import this folder, drop the shots that are out of focus,
match the warm look of the first one on the whole series"* and watch it happen in
the album, slider by slider, with undo shared between you and Claude.

Written in Go with the standard library only. One binary, no Electron, no cloud,
no account: your RAW files never leave your Mac.

- **Claude RAW photo editor** — 28 MCP tools: import, analyze, develop, mask, crop,
  cull, sort, export. Claude sees what it does (renders, histograms, focus maps).
- **Lightroom-style develop** — per-photo, non-destructive settings with a
  camera-matched Standard profile, so a RAW opens looking like the camera's JPEG.
- **Albums as a masonry wall** — drag to reorder, filters by stars and sharpness,
  focus peaking, clipping zebra, virtual copies, notes, several albums.
- **Native Mac app** — a small WebKit window (`Lumen.app`) over a local server;
  photos are shown pixel for pixel, with 100% views from the full sensor data.

> Status: early and moving fast. Sony A7 III (ARW) and DNG are what it has been
> tested on most. See [Limitations](#limitations).

## Contents

- [Why Lumen](#why-lumen)
- [Install](#install)
- [Editing with Claude](#editing-with-claude)
- [Features](#features)
- [How Lumen renders a RAW](#how-lumen-renders-a-raw)
- [Keyboard](#keyboard)
- [Formats](#formats)
- [How it works](#how-it-works)
- [Limitations](#limitations)
- [FAQ](#faq)
- [Contributing](#contributing)
- [License](#license)

## Why Lumen

Photo editors give you sliders; Lumen gives Claude the same sliders. Working a
series of RAW files with an assistant that can look at every frame, judge focus
and exposure, apply a consistent look and leave you the final word is a
different way to edit, and it needs an editor designed for it:

- every operation is an API call with a readable result, so Claude can reason
  about what changed;
- every change is broadcast to the window in real time, with a cursor that shows
  what Claude is working on and an activity log;
- undo and redo are shared, so nothing Claude does is irreversible;
- the develop model follows Lightroom's vocabulary (exposure, highlights,
  shadows, whites, blacks, texture, clarity, dehaze, vibrance, HSL, grading,
  masks with local sliders), so what Claude knows about photo editing transfers.

Lumen is also a capable RAW editor on its own, and free: no subscription, no
catalog lock-in (albums are JSON files pointing at your photos), no telemetry.

## Install

Requirements: macOS 13 or later (Apple Silicon or Intel), [Go](https://go.dev)
1.27+, and Xcode or the Xcode Command Line Tools for the native window
(`swiftc`). [Claude Code](https://claude.com/claude-code) to edit with Claude.

```bash
git clone https://github.com/zonyeh/lumen.git
cd lumen
go build -trimpath -o bin/lumen ./cmd/lumen   # the app server + CLI
macos/build.sh                                 # Lumen.app into ~/Applications
bin/lumen install                              # register the MCP server with Claude Code
open ~/Applications/Lumen.app
```

`Lumen.app` starts the app server when needed and keeps it running; closing the
window leaves the server up for Claude, and the next tool call from Claude opens
the window again. Without Xcode, `bin/lumen` opens the interface in Chrome (app
mode) or your default browser instead.

Lumen listens on `http://127.0.0.1:5866` only and keeps its data in
`~/Pictures/Lumen` (`albums/*.lumen`, `presets/`, `exports/`, `imports/`, `cache/`,
`lumen.log`). Original files are never modified. `cache/` holds a compact preview
(about 5 MB) and a thumbnail per photo; it can be deleted at any time and is
rebuilt as needed.

Environment: `LUMEN_HOME` (data folder), `LUMEN_PORT` (port), `LUMEN_TRASH_DIR`
(tests: "Move to Trash" moves files there instead of the Trash).

## Editing with Claude

After `bin/lumen install`, Claude Code has a `lumen` MCP server. Things you can
say:

- *Import `~/Pictures/2026-10 wedding/RAW` and sort by capture time.*
- *Which photos are out of focus or motion-blurred? Reject them.*
- *Edit the first photo: warm, soft contrast, lifted shadows. Then copy that look
  to the whole series and show me a screenshot of the album.*
- *Add a radial mask on the face and bring the exposure up half a stop.*
- *Export the picks as 2048 px JPEGs to the Desktop.*

Tools: `get_album` (with each photo's sharpness verdict), `get_photo`,
`import_photos`, `render_photo` (with `grid`, `overlay_mask`, `focus`, `clipping`,
`before`, `crop=false`, zoomed `region` at 100%), `screenshot_album`,
`analyze_photo` (histogram, clipping, zones, sharpness), `set_adjustments`
(patch, `mode: add` for nudges), `auto_tone`, `pick_white_balance`,
`reset_adjustments`, `copy_adjustments`, `add_mask`, `update_mask`,
`remove_mask`, `set_crop`, `reorder_photos`, `sort_photos`, `duplicate_photo`,
`remove_items`, `add_note`, `set_metadata`, `presets`, `export_photos`, `undo`,
`redo`, `focus`, `albums`, `batch`.

Every Claude action is shown in the window: a Claude cursor with a name tag moves
to the photo with a label of what it is doing, the card gets an orange outline
and a progress line until its new render arrives, sliders animate to their new
values and the activity timeline records it. With **Follow** on, the view scrolls
to and selects the photo Claude is editing (unless you are busy with the mouse).

Mask coordinates are fractions of the uncropped, oriented photo (x of the width, y
of the height, origin top-left); radial `rx` is a fraction of the width and `ry` of
the height; brush sizes are fractions of the long edge. `render_photo` with
`grid=true` draws that grid so Claude can place shapes precisely.

The MCP server also answers over streamable HTTP at `POST /mcp` for other
clients.

## Features

- **Album**: masonry wall (automatic or 2–8 columns), drag to reorder, sort by
  capture time, name or rating, virtual copies placed next to the original, notes,
  ratings and flags, zoom and scroll, several albums. Undo/redo is shared by you
  and Claude.
- **Camera look**: with the Standard profile a RAW opens the way the camera
  rendered it. Lumen measures the JPEG the camera embedded in the file (the one
  Finder shows) and fits, once per photo while the photo is prepared: brightness,
  contrast and white point of the tone curve; color intensity, a 3x3 color
  correction and a per-hue correction in twelve bins (the camera's own color
  rendering, which a plain camera matrix misses); the camera's grain in flat areas
  as the target of noise reduction at every zoom; and the sharpening that gives
  the camera's crispness on real detail. The noise and sharpening sliders add to
  those built-in amounts. JPEG, PNG and TIFF files open as they are.
- **Develop** (per photo): white balance (temp/tint on a log scale, picker, auto),
  exposure, contrast, highlights, shadows, whites, blacks, texture, clarity, dehaze,
  vibrance, saturation, parametric and point tone curves (RGB/R/G/B), color mixer
  (HSL, 8 bands), black & white mix, color grading wheels, sharpening with
  automatic noise masking, luminance and color noise reduction, lens distortion
  and vignetting, post-crop vignette, grain, calibration, crop/straighten/rotate/
  flip, profiles (standard, neutral, vivid, flat). Applying a preset resets every
  develop setting first (masks included; the crop stays).
- **Masks**: linear and radial gradients, brush (feather, flow, erase), luminance
  range, color range, whole photo; add/subtract/intersect shapes, invert, amount,
  and 17 local sliders.
- **Selection**: a click selects one photo; its check (the circle in its corner)
  adds or removes it; a rectangle dragged on empty space and ⌘A select several.
  With several selected a bar offers Auto tone, Reset, New album (copies with
  their edits), Remove, Move to Trash (asks first; files go to the macOS Trash).
- **Culling**: filters above the album by rating and sharpness (sharp, out of
  focus, motion blur), measured once per photo from the width of its crispest
  edges and how that width depends on direction (camera shake blurs one
  direction). Focus peaking (⇧F) paints in-focus detail green; the clipping zebra
  (J) shows blown highlights in red and crushed shadows in blue.
- **Single photo**: double-click a photo (or Enter) to open it on its own with the
  develop panel. Its RAW is decoded at full resolution in the background; the
  image on screen is computed from the full sensor data at exactly the screen's
  pixel size, and 100% views show real pixels. ← → move through the album, Esc or
  a double-click goes back.
- **Albums area**: every album as a card with covers, counts (photos, edited,
  picks, rejected), capture dates, camera and lens, sharpness counts, size on disk
  and files not found. Open, rename, move to the Trash (only the album file).
- **Export**: JPEG (with EXIF), 16-bit PNG, 16-bit TIFF, full resolution or
  resized, unique file names.

## How Lumen renders a RAW

Pipeline (float32, linear Rec.2020 working space): highlight reconstruction and
white balance in camera space → camera matrix and camera-look color correction →
exposure, local temp/tint, lens vignetting, dehaze (dark channel prior) → local
tone (edge-aware base layer for highlights/shadows) → filmic base curve
(contrast, whites) → blacks and master curve → Oklab: noise reduction, clarity,
texture, sharpening → color mixer, vibrance, saturation, grading, B&W, local
color → sRGB with gamut mapping → channel curves, vignette, grain.

Memory stays bounded however many photos an album holds. Each file is decoded
once, in the background (photos on screen first), into a proxy on disk: a 16-bit
linear copy of about 1500 px built straight from the sensor mosaic, plus the
focus map, sharpness verdict and camera look. Previews render from proxies, and
only a small LRU of them stays in memory. Photos shown larger render from a detail
level (every 2x2 photosite block, half the sensor resolution) decoded on demand;
the photo open on its own, exports and 100% regions use the full sensor
(Malvar-He-Cutler demosaic). The app returns its caches to the system after 90
seconds without activity.

Cards are placed and sized in whole screen pixels (the album is panned with a
translation, never scaled with a CSS transform, which WebKit would blur), and
sharp renders are made at exactly that size with a tent-weighted area filter, so
the browser never resamples a photo. While the view moves only quick renders are
asked for; the sharp ones once it settles. Photos that leave the screen fall back
to the camera's thumbnail and release their render.

## Keyboard

| Key | Action |
|-----|--------|
| V / H / C / M / N | select, pan, crop, masks, note |
| Space (hold) | pan |
| ⌘ scroll / pinch | zoom; two-finger scroll moves around |
| F, ⇧1, ⌘0 | fit selection, fit the album |
| Double-click, Enter | open the photo on its own (Esc or double-click to go back) |
| ← → | previous / next photo (photo open on its own) |
| Z | whole photo / 100% (photo open on its own) |
| ⇧F | focus peaking |
| J | clipping zebra |
| \ | before/after (selected photos, or all) |
| W | white balance picker |
| O | mask overlay |
| [ ] | brush size |
| 0–5, P, X, U | rating, pick, reject, unflag |
| ⇧A | auto tone |
| ⌘Z / ⇧⌘Z | undo / redo |
| ⌘D | virtual copy |
| ⇧⌘C / ⇧⌘V | copy / paste settings |
| ⌘A | select every photo shown |
| ⌘F | search photos by name |
| ⌘E | export |
| ⌫ | remove from album |
| ⌘⌫ | move the selected photos' files to the Trash (asks first) |

Drag a photo onto another to put it before (upper half) or after (lower half).
Double-click a slider label to reset it; click its value to type one.

## Formats

Sony ARW (compressed cRAW, uncompressed, lossless), DNG (uncompressed, lossless
JPEG, lossy/linear including Lightroom smart previews), JPEG, PNG, TIFF. Other
RAWs (CR2/CR3/NEF/RAF/ORF/RW2…) open from their embedded full-size JPEG. An ARW or
DNG whose sensor data cannot be read (a truncated or damaged file) opens from its
embedded preview too, marked "ARW PREVIEW" with a warning.

Color matrices for DNG come from the file; for ARW from the table in
`internal/raw/colordata.go` (unknown camera models fall back to the embedded JPEG).

## How it works

```
cmd/lumen          CLI: open, start, serve, mcp, install, render, info
internal/raw       TIFF parser, ARW/DNG/LJPEG decoders, Sony SR2, EXIF, demosaic,
                   embedded thumbnails, camera color matrices
internal/develop   settings model + registry, pipeline, masks, geometry, auto tone,
                   focus peaking, camera-look fitting, on-disk proxies
internal/doc       album document, masonry layout, undo history, change events
internal/app       operations shared by UI and MCP, render cache, export, presets,
                   albums, trash
internal/mcp       JSON-RPC MCP server, tool definitions, stdio bridge
internal/server    HTTP API, SSE events, embedded UI, MCP endpoint
internal/web/ui    HTML/CSS/ES modules (no build step)
macos              Lumen.app: WebKit window, icon, build script
```

The album order lives in the document; the server computes the masonry positions
after every change, so the UI and Claude always agree on what is where. Renders
are cached by settings hash; the UI requests a size matched to the card on screen,
one request in flight per card, so slider drags update live.

The interface is strictly neutral gray so it never biases color judgment. Only the
photographs, Claude (orange) and alerts such as clipping (red) carry color.

## Limitations

- The look is Lumen's own, fitted to the camera: settings names match Lightroom
  but values do not produce identical results, and Lightroom XMP sidecars are not
  read.
- Demosaicing is Malvar-He-Cutler: at 100% on low-ISO files fine texture is a
  little softer than Apple's or Adobe's engines.
- No AI masks (subject/sky/people), no healing/cloning, no lens profiles.
- Tested on Sony A7 III files and Adobe's DNG samples; other cameras' ARW files
  should work when their model is in the color table.
- Undo history lives in memory and is lost when the app restarts.
- macOS only for the native window; the server and the web UI are plain Go and
  HTML and should run elsewhere, untested.

## FAQ

**Is Lumen a free Lightroom alternative?** It is a free, open-source RAW editor
with Lightroom's develop vocabulary, albums, culling and export. It is not a
catalog manager with keywords and face recognition, and it does not read
Lightroom's edits.

**How does the Claude integration work?** Lumen runs a local MCP server. Claude
Code calls its tools (render, analyze, adjust, mask, crop, export…) and gets
images and text back; the window shows every change as it happens. Nothing is
uploaded by Lumen; what Claude sees is what the tools return to it.

**Does it need Lightroom or Photoshop?** No. Lumen decodes the RAW files itself.

**Which cameras?** Sony ARW and any DNG. Other RAW formats open from their
embedded JPEG for now.

**Where are my edits stored?** In `~/Pictures/Lumen/albums/*.lumen`, plain JSON
with the photo paths and their settings. Your files are never modified.

## Contributing

Issues and pull requests are welcome. Everything is English: code, comments,
interface, documentation. There is no build step for the UI (ES modules, no
framework); `go test ./...` runs the tests, and `internal/develop` has
measurement harnesses (`CompareCamera`, `NoiseDiag`) that compare Lumen's
rendering with the camera's JPEG and with Apple's RAW engine on your own files.

## License

Lumen is released under the MIT License (see `LICENSE`). Third-party material and
its licenses are listed in `NOTICE.md`: the Geist font (SIL OFL 1.1) and the
camera color matrix table generated from LibRaw (LGPL 2.1 / CDDL 1.0).
