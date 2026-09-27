package apiv2

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeAutoscanTaskCommand struct {
	*fakeAdminTasks
	keys    []string
	missing bool
	err     error
}

func (f *fakeAutoscanTaskCommand) GetTaskInfo(key string) taskmanager.TaskInfo {
	f.keys = append(f.keys, "read:"+key)
	if f.missing {
		return taskmanager.TaskInfo{}
	}
	return taskmanager.TaskInfo{Key: key, Name: "Autoscan poll", Category: taskmanager.TaskCategoryLibrary, State: taskmanager.TaskStateIdle}
}
func (f *fakeAutoscanTaskCommand) StartTask(key string) (taskmanager.TaskInfo, error) {
	f.keys = append(f.keys, "start:"+key)
	return taskmanager.TaskInfo{Key: key, Name: "Autoscan poll", Category: taskmanager.TaskCategoryLibrary, State: taskmanager.TaskStateRunning}, f.err
}
func TestAdminAutoscanTrigger(t *testing.T) {
	f := &fakeAutoscanTaskCommand{fakeAdminTasks: newFakeAdminTasks()}
	deps := pilotDeps(nil, nil)
	deps.AdminTasks = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/trigger"
	requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.keys) != 0 {
		t.Fatal("unauthorized dispatch", f.keys)
	}
	rec := do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != 200 || len(f.keys) != 2 || f.keys[0] != "read:autoscan_poll" || f.keys[1] != "start:autoscan_poll" || !strings.Contains(rec.Body.String(), `"execution_scope":"process"`) || !strings.Contains(rec.Body.String(), `"state":"running"`) || rec.Header().Get("Location") != "" {
		t.Fatal(rec.Code, rec.Body.String(), f.keys)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{taskmanager.ErrTaskAlreadyRunning, 409}, {taskmanager.ErrTaskNotFound, 404}, {errors.New("private-provider-address"), 500}} {
		f.err = tc.err
		f.keys = nil
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-provider") || len(f.keys) != 2 {
			t.Fatal(rec.Code, rec.Body.String(), f.keys)
		}
	}
	f.keys = nil
	f.missing = true
	requireProblem(t, do(t, h, "POST", path, "", bearer(adminToken)), TypeNotFound)
	if len(f.keys) != 1 {
		t.Fatal("started missing task", f.keys)
	}
	deps.AdminTasks = nil
	requireProblem(t, do(t, NewHandler(deps), "POST", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestAdminAutoscanTriggerDocument(t *testing.T) {
	paths := generatedDocument(t)["paths"].(map[string]any)
	op := paths[Prefix+"/admin/autoscan/trigger"].(map[string]any)["post"].(map[string]any)
	responses := op["responses"].(map[string]any)
	for _, code := range []string{"200", "409"} {
		if responses[code] == nil {
			t.Fatalf("missing %s", code)
		}
	}
	if responses["202"] != nil {
		t.Fatal("process task must not claim durable job acceptance")
	}
}
