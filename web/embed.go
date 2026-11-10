// Package web embeds the built UI (web/dist) into the Go binary.
//
// Build the UI first (`make ui` or `pnpm -C web build`); without web/dist
// the Go build fails with "pattern all:dist: no matching files found".
package web

import "embed"

// Dist holds the Vite build output under the "dist/" prefix.
//
//go:embed all:dist
var Dist embed.FS
