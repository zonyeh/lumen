# Third-party notices

Lumen is written from scratch in Go with the standard library only. It ships
two things made by others:

## Geist (font)

The interface uses the Geist typeface by Vercel, embedded in
`internal/web/ui/fonts/`. Geist is licensed under the SIL Open Font License 1.1;
the license text is in `internal/web/ui/fonts/LICENSE-Geist.txt`.

## Camera color matrices

`internal/raw/colordata.go` is generated from the `adobe_coeff` table of
LibRaw (`src/tables/colordata.cpp`), which collects, per camera model, the
XYZ-to-camera color matrices published by Adobe in the DNG Converter. LibRaw is
licensed under the LGPL 2.1 or the CDDL 1.0 (https://github.com/LibRaw/LibRaw).
The table is used as data: Lumen contains no LibRaw code.

## Everything else

The RAW decoders (TIFF, Sony ARW including compressed cRAW, lossless JPEG, DNG
including lossy and linear variants), the demosaicing, the color pipeline, the
camera-look fitting, the album, the MCP server and the interface are Lumen's own
and are covered by the license in `LICENSE`.
