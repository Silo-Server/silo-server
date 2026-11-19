// Package nodesourcetest provides a source granter for tests of the code that
// dispatches work to transcode nodes.
package nodesourcetest

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Granter mints a readable stand-in grant without a stat or a key, so a test
// can check which node and file a dispatched request was granted. Err, when
// set, fails every grant.
type Granter struct {
	Err error
}

// ForFile implements nodesource.FileGranter.
func (g Granter) ForFile(_ context.Context, nodeID int, file *models.MediaFile) (string, error) {
	if g.Err != nil {
		return "", g.Err
	}
	if file == nil {
		return "", errors.New("nodesourcetest: no media file")
	}
	return Grant(nodeID, file.ID, file.FilePath), nil
}

// Grant is the grant Granter mints for node nodeID to use media file fileID
// at path.
func Grant(nodeID, fileID int, path string) string {
	return fmt.Sprintf("grant(node=%d file=%d path=%s)", nodeID, fileID, path)
}
