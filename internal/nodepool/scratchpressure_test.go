package nodepool

import (
	"encoding/json"
	"fmt"
)

// scratchStats builds the last_stats blob a node's health response produces for
// a scratch volume at the given fill, in the shape nodemetrics publishes.
func scratchStats(usedGB, totalGB float64, flags ...string) json.RawMessage {
	extra := ""
	for _, flag := range flags {
		extra += fmt.Sprintf(`,"%s":true`, flag)
	}
	return json.RawMessage(fmt.Sprintf(
		`{"system":{"disks":[{"path":"/transcode","used_gb":%g,"total_gb":%g,"scratch":true%s},`+
			`{"path":"/media","used_gb":10,"total_gb":100}]}}`, usedGB, totalGB, extra))
}
