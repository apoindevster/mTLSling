package client

import "multi-cast-transfer/internal/server"

// Re-export small helpers for client use.
var (
	readManifest = server.ReadManifest
	copyExactly  = server.CopyExactly
)
