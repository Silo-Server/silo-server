//go:build silo_node

package web

import "embed"

// DistFS is empty in the transcode node build (tag silo_node), which serves
// no web app.
var DistFS embed.FS
