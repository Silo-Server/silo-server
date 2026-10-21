package apiv2

import "strings"

func adminRecommendationsFixtureCases() []fixtureCase {
	cases := []fixtureCase{{name: "admin_recommendations_status", operationID: "getAdminRecommendationsStatus", method: "GET", path: Prefix + "/admin/recommendations/status", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminRecommendationsStatus", assertHeaders: []string{"Content-Type"}, scenario: "Persisted counts, last runs from any server and process-local running flags have distinct meanings."}}
	for _, tc := range []struct{ suffix, id string }{
		{"embeddings", "triggerAdminRecommendationEmbeddings"}, {"taste-profiles", "triggerAdminRecommendationTasteProfiles"}, {"cowatch", "triggerAdminRecommendationCowatch"}, {"recommendations", "triggerAdminRecommendationRefresh"},
	} {
		cases = append(cases, fixtureCase{name: "admin_recommendations_" + strings.ReplaceAll(tc.suffix, "-", "_"), operationID: tc.id, method: "POST", path: Prefix + "/admin/recommendations/trigger/" + tc.suffix, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminRecommendationStarted", assertHeaders: []string{"Content-Type"}, scenario: "Started acknowledges process-local background execution, not a persisted job."})
	}
	cases = append(cases, fixtureCase{name: "admin_recommendations_embeddings_reset", operationID: "resetAdminRecommendationEmbeddings", method: "POST", path: Prefix + "/admin/recommendations/embeddings/reset", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminRecommendationEmbeddingsReset", assertHeaders: []string{"Content-Type"}, scenario: "A reset reports what it deleted; global rows are kept."})
	return cases
}
