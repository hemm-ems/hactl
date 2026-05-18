// Package docs embeds the hactl manual for use at runtime.
package docs

import _ "embed"

//go:embed manual.md

// Manual contains the full hactl usage manual, embedded at build time.
var Manual string
