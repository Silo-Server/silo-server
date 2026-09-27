package apiv2

import (
	"context"
	"net/http"
)

func registerAdminAutoscanTrigger(reg *Registry) {
	op := humaOp("POST", Prefix+"/admin/autoscan/trigger", "triggerAdminAutoscan", "admin-autoscan", "Reserve and start the autoscan poll task on this process. Returns a process task snapshot, not durable dispatch or scan completion. Enabled settings and per-source intervals still apply; inspect activity for per-source outcomes. No automatic replay.")
	op.Errors = []int{http.StatusConflict}
	Register(reg, Operation{
		Operation: op,
		Class:     ClassActingAdmin, DemoRestricted: isMutatingMethod(op.Method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable,
	}, func(ctx context.Context, _ *struct{}) (*AdminTaskOutput, error) {
		return reg.runAdminTask(ctx, &AdminTaskInput{Key: "autoscan_poll"})
	})
}
