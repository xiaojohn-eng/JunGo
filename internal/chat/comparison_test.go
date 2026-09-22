package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

var historyComparisonResult string

// This benchmark is deliberately source-compatible with ef1a314, so the exact
// same harness can be copied into a clean baseline checkout for an A/B run.
func BenchmarkHistoryComparison(b *testing.B) {
	seed := func(b *testing.B) *Store {
		b.Helper()
		s, err := New(filepath.Join(b.TempDir(), "chat", "messages.json"))
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < 20000; i++ {
			m := Message{ID: fmt.Sprintf("message-%06d", i), DeviceID: "peer", Direction: "incoming", Kind: "text", Text: "A realistic retained device message", Status: "received", Created: time.Unix(int64(i), 0).UTC(), Revision: 1}
			s.messages[m.ID] = m
		}
		if err = s.persist(s.messages); err != nil {
			b.Fatal(err)
		}
		if indexed, ok := any(s).(interface{ rebuildIndexes() }); ok {
			indexed.rebuildIndexes()
		}
		return s
	}
	b.Run("IdleHistoryPoll", func(b *testing.B) {
		s := seed(b)
		var version string
		current, newAPI := any(s).(interface{ JSON(string) (string, error) })
		if newAPI {
			first, err := current.JSON("")
			if err != nil {
				b.Fatal(err)
			}
			var metadata struct{ Version string }
			if err = json.Unmarshal([]byte(first), &metadata); err != nil {
				b.Fatal(err)
			}
			version = metadata.Version
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var err error
			if newAPI {
				historyComparisonResult, err = current.JSON(version)
			} else {
				var encoded []byte
				encoded, err = json.Marshal(map[string]any{"messages": s.List()})
				historyComparisonResult = string(encoded)
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("DurableRevision", func(b *testing.B) {
		s := seed(b)
		m, _ := s.Get("message-000000")
		// Exclude the one-time v1 -> v2 storage marker upgrade from steady state.
		m.Revision++
		if err := s.Put(m); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Revision++
			if err := s.Put(m); err != nil {
				b.Fatal(err)
			}
		}
	})
}
