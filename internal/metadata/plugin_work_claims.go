package metadata

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

// Keys of a book plugin's free-form metadata that describe its work ids.
const (
	pluginMetaWorkEvidence     = "work_evidence"
	pluginMetaWorkLookupFailed = "work_lookup_failed"
)

// splitPluginWorkClaims takes the work-level keys out of a plugin item's
// provider ids for an item of itemType (bookid.HasWorkIDs) and returns the
// edition ids with the answer's claims: each work id with the evidence the
// plugin gave for it in metadata.work_evidence (unknown when absent), and the
// catalogs metadata.work_lookup_failed names. The claims are returned even
// when the answer holds no work id, because that answer replaces the
// capability's earlier claims. Other item types get ids back unchanged and
// nil claims.
func splitPluginWorkClaims(capabilityID, itemType string, ids map[string]string, md *structpb.Struct) (map[string]string, []bookid.WorkClaims) {
	if !bookid.HasWorkIDs(itemType) {
		return ids, nil
	}
	edition, work := bookid.SplitWorkIDs(itemType, ids)
	fields := md.GetFields()
	stated := map[string]string{}
	for name, v := range fields[pluginMetaWorkEvidence].GetStructValue().GetFields() {
		if key, isWork := bookid.WorkKey(name); isWork {
			stated[key] = v.GetStringValue()
		}
	}
	claims := bookid.WorkClaims{Source: capabilityID}
	for key, value := range work {
		claims.Claims = append(claims.Claims, bookid.WorkClaim{
			Provider:   key,
			ProviderID: value,
			Evidence:   bookid.NormalizeEvidence(stated[key]),
		})
	}
	slices.SortFunc(claims.Claims, func(a, b bookid.WorkClaim) int { return strings.Compare(a.Provider, b.Provider) })
	for _, v := range fields[pluginMetaWorkLookupFailed].GetListValue().GetValues() {
		if catalog := strings.ToLower(strings.TrimSpace(v.GetStringValue())); catalog != "" {
			claims.LookupFailed = append(claims.LookupFailed, catalog)
		}
	}
	return edition, []bookid.WorkClaims{claims}
}
