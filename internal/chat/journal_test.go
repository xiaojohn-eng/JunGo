package chat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJournalRecoversConfirmedRevisionsAndDiscardsOnlyTornTail(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	m.Revision++
	m.Completed = 50
	m.Status = "running"
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced(m.ID, m.Revision); err != nil {
		t.Fatal(err)
	}
	durable, err := os.ReadFile(s.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.journalPath(), append(durable, []byte(`{"id":"torn`)...), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := restored.Get(m.ID)
	if got.Completed != 50 || got.SyncedRevision != 2 {
		t.Fatalf("lost confirmed state: %+v", got)
	}
	after, err := os.ReadFile(s.journalPath())
	if err != nil || string(after) != string(durable) {
		t.Fatal("tail recovery changed confirmed records", err)
	}
	m.Revision++
	m.Completed = 100
	m.Status = "complete"
	if err = restored.Put(m); err != nil {
		t.Fatal(err)
	}
	restored, err = New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = restored.Get(m.ID)
	if got.Revision != 3 || got.Completed != 100 {
		t.Fatal("append after recovery lost", got)
	}
}

func TestJournalCompleteCorruptionIsNeverSilentlyDiscarded(t *testing.T) {
	s, name := newStore(t)
	if err := s.Put(example()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("broken record\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(name); err == nil {
		t.Fatal("complete corrupt record was discarded")
	}
}

func TestJournalCompactionCrashWindowAndBoundedReplay(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	m.Revision++
	m.Completed = 50
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced(m.ID, m.Revision); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after durable snapshot replacement but before journal
	// replacement: the old records must not regress the snapshot or its ack.
	if err := s.persist(s.messages); err != nil {
		t.Fatal(err)
	}
	restored, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := restored.Get(m.ID)
	if got.Revision != 2 || got.SyncedRevision != 2 {
		t.Fatal("compaction replay regressed", got)
	}
	// Force the record-count threshold without issuing 1,024 real disk syncs.
	restored.journalRecords = maxJournalRecords
	m.Revision++
	m.Completed = 100
	m.Status = "complete"
	if err = restored.Put(m); err != nil {
		t.Fatal(err)
	}
	if restored.journalRecords != 1 {
		t.Fatal("journal did not compact")
	}
	final, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = final.Get(m.ID)
	if got.Completed != 100 || got.SyncedRevision != 2 {
		t.Fatal("compacted state lost", got)
	}
}

func TestVersionedJSONAndIndexesPreserveHistorySemantics(t *testing.T) {
	s, name := newStore(t)
	m := example()
	m.Direction = "incoming"
	m.Status = "running"
	m.Updated = time.Now()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if s.ActiveIncoming(m.Updated) != 1 || s.ActiveIncoming(m.Updated.Add(time.Minute)) != 0 {
		t.Fatal("active incoming freshness changed")
	}
	response, err := s.JSON("")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Version   string
		Messages  []Message
		Unchanged bool
	}
	if err = json.Unmarshal([]byte(response), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version == "" || len(snapshot.Messages) != 1 {
		t.Fatal("full snapshot omitted fields")
	}
	unchanged, err := s.JSON(snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unchanged, `"unchanged":true`) || strings.Contains(unchanged, `"messages"`) {
		t.Fatal("unchanged poll copied history", unchanged)
	}
	m.Revision++
	m.Status = "complete"
	m.Completed = m.Size
	if err = s.Put(m); err != nil {
		t.Fatal(err)
	}
	changed, _ := s.JSON(snapshot.Version)
	if strings.Contains(changed, `"unchanged":true`) {
		t.Fatal("mutation retained version")
	}
	if s.ActiveIncoming(time.Now()) != 0 {
		t.Fatal("completed file retained active index")
	}
	reopened, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := reopened.JSON(snapshot.Version)
	if strings.Contains(fresh, `"unchanged":true`) {
		t.Fatal("reopen reused old version")
	}
	copy := reopened.List()
	copy[0].Status = "failed"
	again, _ := reopened.JSON("")
	if strings.Contains(again, `"status":"failed"`) {
		t.Fatal("caller mutated cached snapshot")
	}
}

func TestJournalFailureAndSnapshotSizeLimitDoNotPublishMutation(t *testing.T) {
	s, _ := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	m.Revision++
	before, _ := s.JSON("")
	originalJournal, err := os.ReadFile(s.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.journalPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(m); err == nil {
		t.Fatal("journal failure ignored")
	}
	after, _ := s.JSON("")
	if before != after {
		t.Fatal("failed append changed published history")
	}
	if err := os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.journalPath(), originalJournal, 0600); err != nil {
		t.Fatal(err)
	}
	s.snapshotBytes = maxSnapshotBytes
	m.Error = "larger mutable metadata"
	if err := s.Put(m); err == nil {
		t.Fatal("snapshot bound ignored")
	}
	after, _ = s.JSON("")
	if before != after {
		t.Fatal("size rejection changed history")
	}
}

func TestLegacyMigrationAndGracefulDowngradeCheckpoint(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	var disk snapshot
	readVersion := func() int {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &disk); err != nil {
			t.Fatal(err)
		}
		return disk.Version
	}
	if readVersion() != 2 {
		t.Fatal("old binary could read stale base snapshot")
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if readVersion() != 1 || len(disk.Messages) != 1 {
		t.Fatal("downgrade snapshot incomplete")
	}
	if b, err := os.ReadFile(s.journalPath()); err != nil || len(b) != 0 {
		t.Fatal("checkpoint left redundant journal", err)
	}
	legacy, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	m.Revision++
	m.Completed = 75
	if err = legacy.Put(m); err != nil {
		t.Fatal(err)
	}
	if readVersion() != 2 {
		t.Fatal("mutation did not invalidate downgrade snapshot")
	}
	reopened, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(m.ID)
	if got.Completed != 75 {
		t.Fatal("legacy migration lost progress")
	}
}

func TestCompactionFailureRetainsAcknowledgedRecords(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	m.Revision++
	m.Completed = 90
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(s.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(s.journalPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.compactJournal(); err == nil {
		t.Fatal("journal replacement failure ignored")
	}
	m.Revision++
	m.Completed = 100
	if err = s.Put(m); err == nil {
		t.Fatal("uncertain journal accepted further writes")
	}
	if err = os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.journalPath(), journal, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := recovered.Get(m.ID)
	if got.Revision != 2 || got.Completed != 90 {
		t.Fatal("compaction failure lost durable revision", got)
	}
}

func TestCloseSealsMutationsBeforeDowngradeCheckpoint(t *testing.T) {
	s, name := newStore(t)
	m := example()
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	m.Revision++
	if err := s.Put(m); err == nil {
		t.Fatal("late message appended after shutdown checkpoint")
	}
	if err := s.MarkSynced(m.ID, 1); err == nil {
		t.Fatal("late ack appended after shutdown checkpoint")
	}
	reopened, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(m.ID)
	if got.Revision != 1 {
		t.Fatal("late update changed sealed history")
	}
	if err := reopened.Put(m); err != nil {
		t.Fatal("new store instance remained sealed", err)
	}
}

func TestPendingIndexRetainsResumableFilesAndDropsAcknowledgedText(t *testing.T) {
	s, _ := newStore(t)
	text := example()
	text.Kind = "text"
	text.Name = ""
	text.Status = "queued"
	if err := s.Put(text); err != nil {
		t.Fatal(err)
	}
	if len(s.WorkList()) != 1 || !s.HasDevice(text.DeviceID) {
		t.Fatal("new outgoing message missing from index")
	}
	if err := s.MarkSynced(text.ID, 1); err != nil {
		t.Fatal(err)
	}
	if len(s.WorkList()) != 0 {
		t.Fatal("acknowledged text still scanned")
	}
	file := example()
	file.ID = "file"
	file.Status = "cancelled"
	if err := s.Put(file); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced(file.ID, 1); err != nil {
		t.Fatal(err)
	}
	work := s.WorkList()
	if len(work) != 1 || work[0].ID != file.ID {
		t.Fatal("file cleanup updates excluded")
	}
	received := text
	received.ID = "received"
	received.Direction = "incoming"
	if err := s.Put(received); err != nil {
		t.Fatal(err)
	}
	if len(s.WorkList()) != 1 {
		t.Fatal("incoming history entered outgoing work")
	}
}

func TestMissingOrChangedJournalCannotSilentlyLoseConfirmedHistory(t *testing.T) {
	for _, mutation := range []string{"missing", "truncated"} {
		t.Run(mutation, func(t *testing.T) {
			s, name := newStore(t)
			m := example()
			if err := s.Put(m); err != nil {
				t.Fatal(err)
			}
			if mutation == "missing" {
				if err := os.Remove(s.journalPath()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Truncate(s.journalPath(), 0); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "missing" {
				if _, err := New(name); err == nil {
					t.Fatal("version2 loaded without its journal")
				}
			}
			m.Revision++
			if err := s.Put(m); err == nil {
				t.Fatal("changed journal silently accepted another revision")
			}
			got, _ := s.Get(m.ID)
			if got.Revision != 1 {
				t.Fatal("failed append published")
			}
		})
	}
}

func TestInterruptedInitialJournalCreationIsRecoverable(t *testing.T) {
	s, name := newStore(t)
	// The log can exist before the first base snapshot, but must be empty.
	if err := os.WriteFile(s.journalPath(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Put(example()); err != nil {
		t.Fatal(err)
	}
	final, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.List()) != 1 {
		t.Fatal("initial creation lost first message")
	}
}
