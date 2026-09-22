package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func example() Message {
	return Message{ID: "message-1", DeviceID: "device-1", Direction: "outgoing", Kind: "file", Name: "照片.jpg", Size: 100, Status: "queued", Created: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), Revision: 1}
}
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "chat", "messages.json")
	s, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	return s, name
}

func TestPersistenceRevisionsAndLocalAcknowledgement(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	older := m
	older.Status = "failed"
	if err := s.Put(older); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(m.ID)
	if got.Status != "queued" {
		t.Fatal("duplicate revision changed state")
	}
	m.Revision = 2
	m.Status = "running"
	m.Completed = 40
	m.TransferID = "transfer-1"
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced(m.ID, 2); err != nil {
		t.Fatal(err)
	}
	m.Revision = 3
	m.Completed = 100
	m.Status = "complete"
	m.Path = "inbox/message-1.jpg"
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(m.ID)
	if !ok || got.Completed != 100 || got.Status != "complete" || got.SyncedRevision != 2 || got.Path != m.Path {
		t.Fatalf("bad restored message: %+v", got)
	}
	if err := reopened.MarkSynced(m.ID, 4); !errors.Is(err, ErrInvalid) {
		t.Fatal("future ack accepted", err)
	}
	if err := reopened.MarkSynced("unknown", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown ack accepted", err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state is not private", err)
	}
	info, err = os.Stat(filepath.Dir(name))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory is not private", err)
	}
}

func TestIdentityCannotBeChangedAtAnyRevision(t *testing.T) {
	s, _ := newStore(t)
	initial := example()
	if err := s.Put(initial); err != nil {
		t.Fatal(err)
	}
	mutators := []func(*Message){func(m *Message) { m.DeviceID = "other" }, func(m *Message) { m.Direction = "incoming" }, func(m *Message) { m.Kind = "text" }, func(m *Message) { m.Text = "different" }, func(m *Message) { m.Name = "another.jpg" }, func(m *Message) { m.Created = m.Created.Add(time.Second) }}
	for _, mutate := range mutators {
		for _, revision := range []uint64{1, 2} {
			candidate := initial
			candidate.Revision = revision
			mutate(&candidate)
			if err := s.Put(candidate); !errors.Is(err, ErrConflict) {
				t.Fatalf("identity collision accepted: %+v %v", candidate, err)
			}
		}
	}
}

func TestValidationContainsInboxAndLimitsText(t *testing.T) {
	s, _ := newStore(t)
	for _, p := range []string{"../outside", "/tmp/absolute", "one/../../outside", "a\\b", "a//b", ".", "a/./b", "C:/file"} {
		m := example()
		m.Path = p
		if err := s.Put(m); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe path accepted: %q %v", p, err)
		}
	}
	for _, mutate := range []func(*Message){func(m *Message) { m.Text = strings.Repeat("汉", 4097) }, func(m *Message) { m.Name = strings.Repeat("图", 256) }, func(m *Message) { m.Size = MaxFileSize + 1 }, func(m *Message) { m.Completed = 101 }, func(m *Message) { m.Completed = -1 }, func(m *Message) { m.Revision = 0 }, func(m *Message) { m.Status = "unknown" }} {
		m := example()
		mutate(&m)
		if err := s.Put(m); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid message accepted", err)
		}
	}
	m := example()
	m.Text = strings.Repeat("汉", 4096)
	m.Name = strings.Repeat("图", 255)
	if err := s.Put(m); err != nil {
		t.Fatal("valid Unicode limits rejected", err)
	}
}

func TestFailedPersistenceDoesNotPublishMutation(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatal(err)
	}
	m.Revision = 2
	m.Status = "complete"
	m.Completed = m.Size
	if err := s.Put(m); err == nil {
		t.Fatal("persistence failure ignored")
	}
	got, _ := s.Get(m.ID)
	if got.Revision != 1 || got.Status != "queued" {
		t.Fatal("uncommitted update published")
	}
}

func TestConcurrentReadsAndUpdatesAreIsolated(t *testing.T) {
	s, _ := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for revision := uint64(2); revision <= 10; revision++ {
		wg.Add(1)
		go func(revision uint64) {
			defer wg.Done()
			m := example()
			m.Revision = revision
			m.Completed = int64(revision)
			m.Status = "running"
			if err := s.Put(m); err != nil {
				t.Error(err)
			}
			_ = s.List()
		}(revision)
	}
	wg.Wait()
	got, _ := s.Get(m.ID)
	if got.Revision != 10 || got.Completed != 10 {
		t.Fatalf("newest revision lost: %+v", got)
	}
	copy := s.List()
	copy[0].Text = "tampered"
	got, _ = s.Get(m.ID)
	if got.Text != "" {
		t.Fatal("List returned mutable internal state")
	}
}
