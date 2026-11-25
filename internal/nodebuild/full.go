//go:build !silo_node

// Package nodebuild reports whether this binary is a transcode node build.
package nodebuild

// Node reports a transcode node build (tag silo_node); see node.go.
const Node = false
