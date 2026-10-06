package jellycompat

import "testing"

func TestRequestedFieldsNeedDetail_RequiresDetailForRichFields(t *testing.T) {
	requireDetail := []string{
		"providerids",
		"remotetrailers",
		"people",
		"chapters",
		"mediastreams", // requires file detail join we don't yet do
		"mediasources", // pending LATERAL JOIN against media_files (plan §3.2 part b)
	}
	for _, f := range requireDetail {
		fields := map[string]bool{f: true}
		if !requestedFieldsNeedDetail(fields) {
			t.Errorf("field %q should trigger needsDetailFields", f)
		}
	}
}
