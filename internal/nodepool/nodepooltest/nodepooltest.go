// Package nodepooltest builds node pools for tests in other packages.
package nodepooltest

import "github.com/Silo-Server/silo-server/internal/nodepool"

// NewTranscodePool returns an empty transcode pool in which every node counts
// as having a key, so fixtures need not issue one to each node before it can
// be selected. A test about keyless nodes uses nodepool.NewTranscodePool.
func NewTranscodePool() *nodepool.TranscodePool {
	pool := nodepool.NewTranscodePool()
	pool.SetKeyCheck(func(int) bool { return true })
	return pool
}
