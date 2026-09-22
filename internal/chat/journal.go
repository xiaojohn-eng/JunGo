package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

// Each acknowledged revision remains synchronously durable. The append log
// avoids rewriting up to 20,000 unrelated messages for a four-megabyte transfer
// checkpoint. Atomic snapshots bound replay and retain compatibility with v1.
const maxJournalBytes = 8 << 20
const maxJournalRecords = 1024
const maxJournalRecordBytes = 256 << 10

func (s *Store) journalPath() string { return s.path + ".journal" }

func (s *Store) loadJournal() error {
	defer s.rebuildIndexes()
	info, err := os.Lstat(s.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		if s.snapshotVersion == 2 {
			return errors.New("chat journal is missing; refusing an incomplete base snapshot")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("chat journal must be a private regular file (0600)")
	}
	if info.Size() > maxJournalBytes+maxJournalRecordBytes {
		return errors.New("chat journal exceeds its bounded size")
	}
	f, err := os.OpenFile(s.journalPath(), os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, maxJournalRecordBytes)
	var committed int64
	for {
		line, readErr := reader.ReadSlice('\n')
		if readErr == io.EOF {
			// The last unacknowledged append may be torn. Never discard a complete
			// invalid record or any earlier committed revision.
			if len(line) != 0 {
				if err = f.Truncate(committed); err != nil {
					return err
				}
				if err = f.Sync(); err != nil {
					return err
				}
			}
			break
		}
		if readErr != nil {
			return fmt.Errorf("chat journal record: %w", readErr)
		}
		var m Message
		if err = json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &m); err != nil {
			return fmt.Errorf("chat journal is corrupt: %w", err)
		}
		if err = validate(m); err != nil {
			return err
		}
		if previous, exists := s.messages[m.ID]; exists {
			if !sameIdentity(previous, m) {
				return ErrConflict
			}
			// A snapshot can include a prefix of the journal after a crash during
			// compaction. Replay is idempotent, including local acknowledgements.
			if m.Revision > previous.Revision {
				m.SyncedRevision = max(m.SyncedRevision, previous.SyncedRevision)
				s.messages[m.ID] = m
			} else if m.Revision == previous.Revision && m.SyncedRevision > previous.SyncedRevision {
				previous.SyncedRevision = m.SyncedRevision
				s.messages[m.ID] = previous
			}
		} else {
			if len(s.messages) >= MaxMessages {
				return ErrLimit
			}
			s.messages[m.ID] = m
		}
		committed += int64(len(line))
		s.journalRecords++
	}
	s.journalBytes = committed
	return nil
}

func (s *Store) appendMessage(m Message) error {
	if s.journalFailed != nil {
		return s.journalFailed
	}

	// Make the empty log durable before marking a first/legacy snapshot v2.
	// A crash during this upgrade can therefore never create a v2 base without
	// the log needed to distinguish it from an incomplete older snapshot.
	info, err := os.Lstat(s.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		if s.journalBytes > 0 {
			s.journalFailed = errors.New("chat journal disappeared; reopen/recovery is required")
			return s.journalFailed
		}
		if err = secure.WriteFile(s.journalPath(), nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("chat journal must be a private regular file (0600)")
	} else if info.Size() != s.journalBytes {
		s.journalFailed = errors.New("chat journal changed outside this store; reopen/recovery is required")
		return s.journalFailed
	}
	info, err = os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if len(s.messages) > 0 {
			s.journalFailed = errors.New("chat base snapshot disappeared; reopen/recovery is required")
			return s.journalFailed
		}
		if err = s.persist(s.messages); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("chat state must be a private regular file (0600)")
	}

	if s.snapshotVersion != 2 {
		// Older binaries reject this version rather than silently loading an old
		// base snapshot and overlooking its newer durable journal records.
		if err = s.persist(s.messages); err != nil {
			return err
		}
	}
	if s.journalBytes >= maxJournalBytes || s.journalRecords >= maxJournalRecords {
		if err = s.compactJournal(); err != nil {
			return err
		}
	}
	record, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(record)+1 > maxJournalRecordBytes {
		return fmt.Errorf("%w: chat journal record too large", ErrLimit)
	}
	nextBytes := s.snapshotBytes + int64(len(record)-s.messageBytes[m.ID])
	if _, exists := s.messages[m.ID]; !exists && len(s.messages) > 0 {
		nextBytes++
	}
	if nextBytes > maxSnapshotBytes {
		return fmt.Errorf("%w: chat snapshot exceeds its bounded size", ErrLimit)
	}
	record = append(record, '\n')
	f, err := os.OpenFile(s.journalPath(), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := f.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		// Prevent a failed partial record from becoming the middle of a later
		// valid journal. If rollback itself fails, reopen/recovery is required.
		rollbackErr := f.Truncate(s.journalBytes)
		if rollbackErr == nil {
			rollbackErr = f.Sync()
		}
		if rollbackErr != nil {
			s.journalFailed = fmt.Errorf("chat journal needs recovery: %w", errors.Join(err, rollbackErr))
		}
		return err
	}
	s.snapshotBytes = nextBytes
	s.messageBytes[m.ID] = len(record) - 1
	s.journalBytes += int64(n)
	s.journalRecords++
	return nil
}

func (s *Store) compactJournal() error {
	if err := s.persist(s.messages); err != nil {
		return err
	}
	// Snapshot is durable before journal replacement. Either the previous log
	// or the empty one is safe to replay after any interruption.
	if err := secure.WriteFile(s.journalPath(), nil); err != nil {
		// Rename may have happened before a directory-sync error. Stop appending
		// until recovery so the in-memory byte offset cannot point past EOF.
		s.journalFailed = fmt.Errorf("chat journal compaction needs recovery: %w", err)
		return err
	}
	s.journalBytes = 0
	s.journalRecords = 0
	return nil
}

// Checkpoint prepares the store for downgrade: the complete current history is
// written in the legacy format before the redundant journal is cleared. A later
// mutation upgrades the marker again before it appends anything.
func (s *Store) Checkpoint() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointLocked()
}

// Close seals mutations before checkpointing. Requests which passed an outer
// engine/context check before shutdown cannot append after its downgrade point.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.checkpointLocked()
}
func (s *Store) checkpointLocked() error {
	if s.journalFailed != nil {
		return s.journalFailed
	}
	if err := s.persistVersion(s.messages, 1); err != nil {
		return err
	}
	if err := secure.WriteFile(s.journalPath(), nil); err != nil {
		s.journalFailed = fmt.Errorf("chat checkpoint needs recovery: %w", err)
		return err
	}
	s.journalBytes = 0
	s.journalRecords = 0
	return nil
}
