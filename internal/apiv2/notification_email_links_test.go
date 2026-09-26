package apiv2

import "testing"

func TestNotificationEmailLinkDeclarations(t *testing.T) {
	doc := generatedDocument(t)
	paths := doc["paths"].(map[string]any)
	for _, tc := range []struct{ path, method, id string }{
		{"verify", "get", "verifyNotificationEmailAddress"},
		{"unsubscribe", "get", "unsubscribeNotificationEmail"},
		{"unsubscribe", "post", "unsubscribeNotificationEmailOneClick"},
	} {
		op := paths[Prefix+"/notifications/email/"+tc.path].(map[string]any)[tc.method].(map[string]any)
		if op["operationId"] != tc.id || op["x-silo-raw-protocol"] != "html-callback" || op["requestBody"] != nil {
			t.Fatalf("callback declaration: %v", op)
		}
		if security, ok := op["security"].([]any); ok && len(security) != 0 {
			t.Fatal("callback requires browser login")
		}
		responses := op["responses"].(map[string]any)
		for _, status := range []string{"200", "400", "500"} {
			response := responses[status].(map[string]any)
			content := response["content"].(map[string]any)
			if content["text/html"] == nil || content["application/json"] != nil {
				t.Fatalf("%s %s lost HTML", tc.id, status)
			}
		}
		if tc.path == "verify" && responses["409"] == nil {
			t.Fatal("missing address conflict")
		}
		if tc.method == "post" && op[extRetrySafety] != string(RetrySafetyNonRetryable) {
			t.Fatal("one-click retry guarantee invented")
		}
	}
}
