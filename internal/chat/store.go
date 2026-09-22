// Package chat persists revisioned messages between a user's paired devices.
// Transport authentication, inbox bytes, and transfer execution belong to the
// engine; this package never trusts a remote filesystem path as a local path.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

const MaxMessages = 20000
const MaxFileSize int64 = 1 << 40
const maxSnapshotBytes = 512 << 20

var (
	ErrInvalid  = errors.New("invalid chat message")
	ErrConflict = errors.New("chat message identity conflict")
	ErrLimit    = errors.New("chat message limit reached")
	ErrNotFound = errors.New("chat message not found")
)

type Message struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"deviceId"`
	Direction  string    `json:"direction"`
	Kind       string    `json:"kind"`
	Text       string    `json:"text"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	Completed  int64     `json:"completed"`
	Status     string    `json:"status"`
	TransferID string    `json:"transferId"`
	Path       string    `json:"path"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Error      string    `json:"error"`
	Revision   uint64    `json:"revision"`
	// SyncedRevision is local bookkeeping and must be stripped by the engine
	// before sending a Message over the wire.
	SyncedRevision uint64 `json:"syncedRevision,omitempty"`
}

type Store struct {
	mu              sync.RWMutex
	path            string
	messages        map[string]Message
	orderedIDs      []string
	incoming        map[string]Message
	work            map[string]Message
	devices         map[string]bool
	revision        uint64
	instance        string
	cachedJSON      string
	journalBytes    int64
	journalRecords  int
	journalFailed   error
	snapshotBytes   int64
	messageBytes    map[string]int
	snapshotVersion int
	closed          bool
}

type snapshot struct {
	Version  int       `json:"version"`
	Messages []Message `json:"messages"`
}

func New(name string) (*Store, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("chat store path is required")
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("chat state directory must be private (0700)")
	}
	s := &Store{path: abs, messages: make(map[string]Message), incoming: make(map[string]Message), instance: secure.Random(12)}
	info, err = os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		// A nonempty log requires its base snapshot: after compaction it no longer
		// contains the entire history, so silently replaying it would lose records.
		journal, journalErr := os.Lstat(s.journalPath())
		if journalErr == nil && journal.Size() > 0 {
			return nil, errors.New("chat journal is missing its base snapshot")
		}
		if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
			return nil, journalErr
		}
		if err = s.loadJournal(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("chat state must be a private regular file (0600)")
	}
	if info.Size() > maxSnapshotBytes {
		return nil, errors.New("chat snapshot exceeds its bounded size")
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var saved snapshot
	if err = json.Unmarshal(b, &saved); err != nil {
		return nil, fmt.Errorf("chat state is corrupt: %w", err)
	}
	if saved.Version != 1 && saved.Version != 2 {
		return nil, errors.New("unsupported chat snapshot version")
	}
	s.snapshotVersion = saved.Version
	if len(saved.Messages) > MaxMessages {
		return nil, ErrLimit
	}
	for _, message := range saved.Messages {
		if err = validate(message); err != nil {
			return nil, err
		}
		if _, exists := s.messages[message.ID]; exists {
			return nil, ErrConflict
		}
		s.messages[message.ID] = message
	}
	if err = s.loadJournal(); err != nil {
		return nil, err
	}
	if s.snapshotBytes > maxSnapshotBytes {
		return nil, ErrLimit
	}
	return s, nil
}

func (s *Store) Put(message Message) error {
	if err := validate(message); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("chat store is closed")
	}
	previous, exists := s.messages[message.ID]
	if exists {
		if !sameIdentity(previous, message) {
			return ErrConflict
		}
		if message.Revision <= previous.Revision {
			return nil
		}
		// Incoming updates cannot erase a previously acknowledged local revision.
		message.SyncedRevision = max(previous.SyncedRevision, message.SyncedRevision)
	} else if len(s.messages) >= MaxMessages {
		return ErrLimit
	}
	if err := s.appendMessage(message); err != nil {
		return err
	}
	s.publish(message)
	return nil
}

func (s *Store) MarkSynced(id string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("chat store is closed")
	}
	message, ok := s.messages[id]
	if !ok {
		return ErrNotFound
	}
	if revision == 0 || revision > message.Revision {
		return fmt.Errorf("%w: acknowledged revision is outside the message history", ErrInvalid)
	}
	if revision <= message.SyncedRevision {
		return nil
	}
	message.SyncedRevision = revision
	if err := s.appendMessage(message); err != nil {
		return err
	}
	s.publish(message)
	return nil
}

func (s *Store) Get(id string) (Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	message, ok := s.messages[id]
	return message, ok
}
func (s *Store) List() []Message { s.mu.RLock(); defer s.mu.RUnlock(); return s.listLocked() }
func (s *Store) listLocked() []Message {
	result := make([]Message, 0, len(s.orderedIDs))
	for _, id := range s.orderedIDs {
		result = append(result, s.messages[id])
	}
	return result
}

// JSON returns a versioned immutable snapshot. Unchanged polls do not copy,
// sort or encode the retained history; old clients still receive the full list.
func (s *Store) JSON(version string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.instance + ":" + strconv.FormatUint(s.revision, 10)
	if version == current {
		return `{"version":"` + current + `","unchanged":true}`, nil
	}
	if s.cachedJSON == "" {
		b, err := json.Marshal(struct {
			Version  string    `json:"version"`
			Messages []Message `json:"messages"`
		}{current, s.listLocked()})
		if err != nil {
			return "", err
		}
		s.cachedJSON = string(b)
	}
	return s.cachedJSON, nil
}

// ActiveIncoming examines only currently active received file messages.
func (s *Store) ActiveIncoming(now time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, m := range s.incoming {
		if now.Sub(m.Updated) < time.Minute {
			count++
		}
	}
	return count
}

func (s *Store) rebuildIndexes() {
	s.orderedIDs = s.orderedIDs[:0]
	s.incoming = make(map[string]Message)
	s.work = make(map[string]Message)
	s.devices = make(map[string]bool)
	s.snapshotBytes = int64(len(`{"version":1,"messages":[]}`))
	s.messageBytes = make(map[string]int, len(s.messages))
	for _, m := range ordered(s.messages) {
		s.orderedIDs = append(s.orderedIDs, m.ID)
		s.indexIncoming(m)
		encoded, _ := json.Marshal(m)
		s.messageBytes[m.ID] = len(encoded)
		s.snapshotBytes += int64(len(encoded))
	}
	if len(s.messages) > 0 {
		s.snapshotBytes += int64(len(s.messages) - 1)
	}
	s.cachedJSON = ""
}

// WorkList excludes received history and already acknowledged text messages.
// File messages remain indexed so local retries and cancellation cleanup can
// still advance their state, including after a previous terminal notification.
func (s *Store) WorkList() []Message      { s.mu.RLock(); defer s.mu.RUnlock(); return ordered(s.work) }
func (s *Store) HasDevice(id string) bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.devices[id] }

func (s *Store) indexIncoming(m Message) {
	s.devices[m.DeviceID] = true
	if m.Direction == "outgoing" && (m.Kind == "file" || m.SyncedRevision < m.Revision) {
		s.work[m.ID] = m
	} else {
		delete(s.work, m.ID)
	}
	if m.Direction == "incoming" && m.Kind == "file" && (m.Status == "running" || m.Status == "hashing") {
		s.incoming[m.ID] = m
	} else {
		delete(s.incoming, m.ID)
	}
}
func (s *Store) publish(m Message) {
	if _, exists := s.messages[m.ID]; !exists {
		at := sort.Search(len(s.orderedIDs), func(i int) bool {
			other := s.messages[s.orderedIDs[i]]
			return other.Created.After(m.Created) || other.Created.Equal(m.Created) && other.ID >= m.ID
		})
		s.orderedIDs = append(s.orderedIDs, "")
		copy(s.orderedIDs[at+1:], s.orderedIDs[at:])
		s.orderedIDs[at] = m.ID
	}
	s.messages[m.ID] = m
	s.indexIncoming(m)
	s.revision++
	s.cachedJSON = ""
}

func (s *Store) persist(messages map[string]Message) error { return s.persistVersion(messages, 2) }
func (s *Store) persistVersion(messages map[string]Message, version int) error {
	b, err := json.Marshal(snapshot{Version: version, Messages: ordered(messages)})
	if err != nil {
		return err
	}
	if len(b) > maxSnapshotBytes {
		return fmt.Errorf("%w: chat snapshot exceeds its bounded size", ErrLimit)
	}
	if err = secure.WriteFile(s.path, b); err != nil {
		return err
	}
	s.snapshotVersion = version
	return nil
}
func ordered(messages map[string]Message) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		result = append(result, message)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Created.Equal(result[j].Created) {
			return result[i].ID < result[j].ID
		}
		return result[i].Created.Before(result[j].Created)
	})
	return result
}
func sameIdentity(a, b Message) bool {
	return a.ID == b.ID && a.DeviceID == b.DeviceID && a.Direction == b.Direction && a.Kind == b.Kind && a.Text == b.Text && a.Name == b.Name && a.Created.Equal(b.Created)
}

func validate(m Message) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }
	if !identifier(m.ID) || !identifier(m.DeviceID) {
		return invalid("stable message and device IDs are required")
	}
	if m.Direction != "incoming" && m.Direction != "outgoing" {
		return invalid("unknown direction")
	}
	if m.Kind != "text" && m.Kind != "file" {
		return invalid("unknown message kind")
	}
	if !utf8.ValidString(m.Text) || utf8.RuneCountInString(m.Text) > 4096 {
		return invalid("text exceeds 4096 characters")
	}
	if !utf8.ValidString(m.Name) || utf8.RuneCountInString(m.Name) > 255 || strings.ContainsRune(m.Name, 0) {
		return invalid("invalid filename")
	}
	if m.Size < 0 || m.Size > MaxFileSize || m.Completed < 0 || m.Completed > m.Size {
		return invalid("invalid byte counts")
	}
	if !safeRelativePath(m.Path) {
		return invalid("inbox path must be relative and contained")
	}
	if m.Revision == 0 || m.SyncedRevision > m.Revision {
		return invalid("invalid revision")
	}
	if len(m.TransferID) > 256 || len(m.Error) > 16<<10 {
		return invalid("metadata exceeds limit")
	}
	switch m.Status {
	case "queued", "waiting", "hashing", "running", "paused", "cancelled", "failed", "complete", "sent", "received":
	default:
		return invalid("unknown message status")
	}
	return nil
}
func identifier(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 33 || r == 127 || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
func safeRelativePath(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 || !utf8.ValidString(value) || path.IsAbs(value) || strings.ContainsAny(value, "\\\x00:") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
