package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// Every listing of a destination is bounded: anyone who can write a bucket
// (or answer for its endpoint) could otherwise hold enough keys, or page
// without end, to exhaust the server's memory on every scheduled backup.
// Readers page through Destination.ListPage, stop at a cap, and skip a
// folder they do not read by listing after it, as a delimiter would.
const (
	// listPageSize is the most objects one listing request asks for.
	listPageSize = 1000
	// maxBackupFolders bounds the backup folders of one server a listing
	// reads; rotation keeps at most config.MaxBackupKeep of each kind.
	maxBackupFolders = 2000
	// maxFolderObjects bounds the objects of one backup folder. A backup
	// writes four; a folder with more than this was not written by Silo.
	maxFolderObjects = 32
	// maxListRequests bounds the requests one folder listing makes.
	maxListRequests = 1000
	// maxOtherServers bounds the other servers' folders a listing names.
	maxOtherServers = 100
	// maxUploadObjects bounds the upload objects one sweep reads.
	maxUploadObjects = 100_000
)

// ErrTooManyObjects refuses a listing beyond its cap.
var ErrTooManyObjects = errors.New("the backup destination holds more objects there than Silo writes")

// skipFolder is the after that lists past every key under prefix+name+"/":
// "0" sorts right after "/".
func skipFolder(prefix, name string) string { return prefix + name + "0" }

// listObjects lists every object under prefix, a page at a time, and
// refuses more than limit.
func listObjects(ctx context.Context, d Destination, prefix string, limit int) ([]blobstore.ObjectInfo, error) {
	var out []blobstore.ObjectInfo
	after := ""
	for {
		page, next, err := d.ListPage(ctx, prefix, after, min(listPageSize, limit-len(out)+1))
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		switch {
		case len(out) > limit:
			return nil, fmt.Errorf("%w: more than %d objects under %s", ErrTooManyObjects, limit, prefix)
		case next == "":
			return out, nil
		case next <= after:
			return nil, errors.New("the backup destination's listing did not advance")
		}
		after = next
	}
}

// hasObjects reports whether anything is under prefix.
func hasObjects(ctx context.Context, d Destination, prefix string) (bool, error) {
	page, _, err := d.ListPage(ctx, prefix, "", 1)
	return len(page) > 0, err
}

// folderListing is one backup folder's objects, as listBackupFolders read
// them. overflow is set when it holds more than maxFolderObjects, which
// were not all read.
type folderListing struct {
	id       string
	objects  []blobstore.ObjectInfo
	overflow bool
}

// listBackupFolders lists the backup folders under prefix (one server's)
// in key order, each with its objects. A child that is not a backup folder
// (a full restore's overwritten files, which may be many) is skipped
// without reading it, as is the rest of a folder past maxFolderObjects. It
// refuses more than maxBackupFolders folders or maxListRequests requests.
func listBackupFolders(ctx context.Context, d Destination, prefix string) ([]folderListing, error) {
	var out []folderListing
	after := ""
	for range maxListRequests {
		page, next, err := d.ListPage(ctx, prefix, after, listPageSize)
		if err != nil {
			return nil, err
		}
		skipTo := ""
		for _, obj := range page {
			name, _, inFolder := strings.Cut(strings.TrimPrefix(obj.Key, prefix), "/")
			if !inFolder {
				continue
			}
			if _, _, ok := ParseFolderName(name); !ok {
				skipTo = skipFolder(prefix, name)
				break
			}
			if n := len(out); n == 0 || out[n-1].id != name {
				if n == maxBackupFolders {
					return nil, fmt.Errorf("%w: more than %d backup folders under %s", ErrTooManyObjects, maxBackupFolders, prefix)
				}
				out = append(out, folderListing{id: name})
			}
			f := &out[len(out)-1]
			if len(f.objects) == maxFolderObjects {
				f.overflow, skipTo = true, skipFolder(prefix, name)
				break
			}
			f.objects = append(f.objects, obj)
		}
		switch {
		case skipTo != "":
			after = skipTo
		case next == "":
			return out, nil
		case next <= after:
			return nil, errors.New("the backup destination's listing did not advance")
		default:
			after = next
		}
	}
	return nil, fmt.Errorf("%w: listing %s took more than %d requests", ErrTooManyObjects, prefix, maxListRequests)
}

// childFolders names up to limit folders directly under prefix, in key
// order, one request each: it lists one object and then skips its folder.
func childFolders(ctx context.Context, d Destination, prefix string, limit int) ([]string, error) {
	var names []string
	after := ""
	for range 2 * limit {
		page, _, err := d.ListPage(ctx, prefix, after, 1)
		if err != nil || len(page) == 0 {
			return names, err
		}
		name, _, inFolder := strings.Cut(strings.TrimPrefix(page[0].Key, prefix), "/")
		if !inFolder {
			after = page[0].Key
			continue
		}
		names = append(names, name)
		if len(names) == limit {
			break
		}
		after = skipFolder(prefix, name)
	}
	return names, nil
}
