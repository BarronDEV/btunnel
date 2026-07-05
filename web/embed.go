package web

import "embed"

// StaticFiles embeds the entire web frontend directory into the binary.
// This allows the embedded signaling server to serve web frontend files
// without requiring the physical "web/" folder at runtime.
//
//go:embed index.html sw.js webrtc-client.js
var StaticFiles embed.FS
