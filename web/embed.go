//go:build !silo_node

package web

import "embed"

//go:embed all:dist
var DistFS embed.FS
