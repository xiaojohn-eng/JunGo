package engine

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestClosedEngineRejectsVPNAndConsumesFD(t *testing.T) {
	e, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.StartVPN(fd); err == nil {
		t.Fatal("closed engine restarted")
	}
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != syscall.EBADF {
		t.Fatalf("transferred FD remains open: %v", err)
	}
	if _, err = e.Request(`{"method":"upload","params":{"deviceId":"device","shareId":"share","path":"a","source":"/tmp/a"}}`); err == nil {
		t.Fatal("closed engine accepted a new task")
	}
}

func TestRestartPreservesExplicitPauseAndCancel(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	up, err := e.addTransfer(Transfer{Direction: "upload", DeviceID: "offline", ShareID: "share", Path: "a.bin", Source: "/missing/source"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.transferAction(up.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	down, err := e.addTransfer(Transfer{Direction: "download", DeviceID: "offline", ShareID: "share", Path: "b.bin", Destination: dir + "/b.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.transferAction(down.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.wakeTransfers()
	time.Sleep(100 * time.Millisecond)
	states := map[string]string{}
	for _, task := range reopened.transferSnapshot() {
		states[task.ID] = task.Status
	}
	if states[up.ID] != "paused" || states[down.ID] != "cancelled" {
		t.Fatalf("explicit states lost: %v", states)
	}
	if err = reopened.transferAction(down.ID, "resume"); err == nil {
		t.Fatal("cancelled task restarted")
	}
	if err = reopened.transferAction(down.ID, "pause"); err == nil {
		t.Fatal("cancelled task became resumable through pause")
	}
	if err = reopened.transferAction(down.ID, "cancel"); err != nil {
		t.Fatal("repeated cancellation was not idempotent", err)
	}
}

func TestRejectedBypassPolicyDoesNotPersist(t *testing.T) {
	e, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Request(`{"method":"network","params":{"bypassUIDs":[-1]}}`); err == nil {
		t.Fatal("invalid UID accepted")
	}
	if len(e.State().BypassUIDs) != 0 {
		t.Fatal("rejected policy persisted")
	}
}
