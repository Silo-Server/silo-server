package taskmanager_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/taskmanager/triggers"
)

func TestStartupScheduleInstalledDuringRunFiresAfterward(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "scheduled"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				task := gatedTask{
					stubTask: stubTask{key: "plugin:7:sync", triggers: []taskmanager.TriggerConfig{
						{Type: taskmanager.TriggerTypeInterval, IntervalMs: 1_000},
					}},
					entered: make(chan struct{}, 1), release: make(chan struct{}), runs: make(chan struct{}, 2),
				}
				manager := taskmanager.New(&fakeTriggerRepository{}, fakeExecutionRepository{}, triggers.New, nil)
				manager.Register(task)
				manager.Start(ctx)
				defer manager.Stop()
				if manual {
					if _, err := manager.StartTask(task.Key()); err != nil {
						t.Fatal(err)
					}
				}
				<-task.entered
				if err := manager.UpdateTriggers(task.Key(), []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(6 * time.Second) //nolint:forbidigo // Advance past the real startup trigger's delay on the fake clock.
				close(task.release)
				<-task.runs
				select {
				case <-task.runs:
				case <-time.After(time.Minute):
					t.Fatal("startup schedule did not run after the earlier execution finished")
				}
			})
		})
	}
}
