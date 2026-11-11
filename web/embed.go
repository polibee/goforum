package web

import "embed"

// FS contains templates and built static assets so the server ships as one binary.
//
//go:embed templates static locales
var FS embed.FS
