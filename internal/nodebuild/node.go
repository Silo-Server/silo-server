//go:build silo_node

// Package nodebuild reports whether this binary is a transcode node build.
package nodebuild

// Node reports a transcode node build (tag silo_node): no image
// processing and no web app, so it runs only as a transcode node.
const Node = true
