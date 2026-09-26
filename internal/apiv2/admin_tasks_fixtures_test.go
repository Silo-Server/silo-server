package apiv2

func adminTasksFixtureCases() []fixtureCase {
	const base = Prefix + "/admin/tasks/fixture"
	const schema = "#/components/schemas/"
	get := func(name, operation, path, bodySchema, scenario string) fixtureCase {
		return fixtureCase{name: name, operationID: operation, method: "GET", path: path, headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + bodySchema, scenario: scenario}
	}
	return []fixtureCase{
		get("admin_tasks", "listAdminTasks", Prefix+"/admin/tasks", "CollectionAdminTask", "Finite registered tasks report process-local state."),
		get("admin_task", "getAdminTask", base, "AdminTask", "Canonical runtime task state does not imply durable execution."),
		get("admin_task_schedule", "getAdminTaskSchedule", base+"/triggers", "AdminTaskSchedule", "Persisted schedule configuration is separate from runtime state."),
		get("admin_task_history", "listAdminTaskHistory", base+"/history?limit=2", "CollectionAdminTaskExecution", "Execution history uses string identities and signed continuation state."),
		get("admin_task_metrics", "getAdminTaskMetrics", Prefix+"/admin/tasks/refresh_metadata/metrics", "AdminTaskMetrics", "Bounded debt samples and metric groups use typed fields."),
		get("admin_jobs", "listAdminJobs", Prefix+"/admin/jobs?limit=2", "CollectionAdminTaskJob", "Administrator jobs use bounded pages and safe typed projections."),
		get("admin_job", "getAdminJob", Prefix+"/admin/jobs/a", "AdminTaskJob", "Job details omit raw request payloads and internal diagnostics."),
		{name: "admin_task_started", operationID: "runAdminTask", method: "POST", path: base + "/run", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + "AdminTask", scenario: "HTTP 200 acknowledges reserved local work without a durable accepted job."},
		{name: "admin_task_cancel", operationID: "cancelAdminTask", method: "POST", path: base + "/cancel", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + "AdminTask", scenario: "Cancellation reports process-local state; prior effects remain."},
		{name: "admin_task_schedule_updated", operationID: "updateAdminTaskSchedule", method: "PUT", path: base + "/triggers", headers: with(bearer(adminToken), "If-Match", "*"), body: `{"triggers":[]}`, status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: schema + "AdminTaskSchedule", scenario: "An empty persisted schedule remains configured across restarts."},
		{name: "admin_task_schedule_guard_required", operationID: "updateAdminTaskSchedule", method: "PUT", path: base + "/triggers", headers: bearer(adminToken), body: `{"triggers":[]}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: schema + "Problem", scenario: "Schedule edits require the captured validator."},
	}
}
