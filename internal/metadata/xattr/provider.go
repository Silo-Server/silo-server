package xattr

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Provider reads metadata hints from extended file attributes.
type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Slug() string       { return "xattr" }
func (p *Provider) Name() string       { return "Extended Attributes" }
func (p *Provider) ForTypes() []string { return []string{"movie", "series"} }

func (p *Provider) Search(ctx context.Context, query metadata.SearchQuery) ([]metadata.SearchResult, error) {
	filePath := firstAvailablePath(query.FilePath, query.RepresentativeFilePath, query.ProviderIDs["_filepath"])
	if filePath == "" && len(query.AllGroupFilePaths) > 0 {
		filePath = query.AllGroupFilePaths[0]
	}
	if filePath == "" {
		return nil, nil
	}
	ids := readXattrIDs(filePath)
	if len(ids) == 0 {
		return nil, nil
	}
	return []metadata.SearchResult{{
		ProviderIDs: ids,
		Provider:    p.Slug(),
	}}, nil
}

func (p *Provider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	filePath := firstAvailablePath(req.FilePath, req.RepresentativeFilePath)
	if filePath == "" && len(req.AllGroupFilePaths) > 0 {
		filePath = req.AllGroupFilePaths[0]
	}
	if filePath == "" {
		return nil, nil
	}
	ids := readXattrIDs(filePath)
	if len(ids) == 0 {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{
		HasMetadata: true,
		ProviderIDs: ids,
	}, nil
}

func firstAvailablePath(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func readXattrIDs(path string) map[string]string {
	ids := make(map[string]string)
	if v, err := getxattr(path, "user.metadb.id"); err == nil && v != "" {
		ids["metadb"] = v
	}
	if v, err := getxattr(path, "user.metadb.hash"); err == nil && v != "" {
		ids["oshash"] = v
	}
	return ids
}

func getxattr(path, attr string) (string, error) {
	sz, err := unix.Getxattr(path, attr, nil)
	if err != nil {
		return "", err
	}
	if sz == 0 {
		return "", nil
	}
	buf := make([]byte, sz)
	n, err := unix.Getxattr(path, attr, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func isNoData(err error) bool {
	return errors.Is(err, errNoAttr) || errors.Is(err, unix.ENOTSUP)
}
