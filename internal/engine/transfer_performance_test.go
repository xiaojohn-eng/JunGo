package engine

import (
	"fmt"
	"testing"
	"time"
)

func TestRunnableSnapshotExcludesHistoryAndUserStoppedTasks(t *testing.T) {
	e := &Engine{tasks: map[string]*Transfer{
		"queued":    {ID: "queued", Status: "queued", Created: time.Unix(1, 0)},
		"waiting":   {ID: "waiting", Status: "waiting", Created: time.Unix(2, 0)},
		"cleanup":   {ID: "cleanup", Status: "cancelled", CancelRemote: true, Created: time.Unix(3, 0)},
		"cancelled": {ID: "cancelled", Status: "cancelled"},
		"paused":    {ID: "paused", Status: "paused"},
		"complete":  {ID: "complete", Status: "complete"},
		"running":   {ID: "running", Status: "running"},
	}}
	got := e.runnableTransfers()
	if len(got) != 3 || got[0].ID != "queued" || got[1].ID != "waiting" || got[2].ID != "cleanup" {
		t.Fatalf("runnable selection changed cancellation/order semantics: %+v", got)
	}
	got[0].Status = "complete"
	if e.tasks["queued"].Status != "queued" {
		t.Fatal("snapshot aliases mutable task")
	}
}

func BenchmarkRetainedTransferQueue(b *testing.B) {
	e := &Engine{tasks: make(map[string]*Transfer, 10000)}
	for i := 0; i < 10000; i++ {
		id := fmt.Sprint(i)
		e.tasks[id] = &Transfer{ID: id, Status: "complete", Created: time.Unix(int64(i), 0)}
	}
	e.tasks["active"] = &Transfer{ID: "active", Status: "queued", Created: time.Now()}
	b.Run("PreviousFullHistoryScan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, task := range e.transferSnapshot() {
				if task.Status == "queued" {
					_ = task.ID
				}
			}
		}
	})
	b.Run("RunnableOnly", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(e.runnableTransfers()) != 1 {
				b.Fatal("task missing")
			}
		}
	})
}
