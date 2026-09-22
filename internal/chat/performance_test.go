package chat

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func benchmarkStore(b *testing.B, count int) *Store {
	b.Helper()
	s, err := New(filepath.Join(b.TempDir(), "chat", "messages.json"))
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		m := Message{ID: fmt.Sprintf("message-%06d", i), DeviceID: "peer", Direction: "incoming", Kind: "text", Text: "A realistic retained device message", Status: "received", Created: time.Unix(int64(i), 0).UTC(), Revision: 1}
		s.messages[m.ID] = m
	}
	if err = s.persist(s.messages); err != nil {
		b.Fatal(err)
	}
	s.rebuildIndexes()
	return s
}

func BenchmarkRetainedHistory(b *testing.B) {
	for _, count := range []int{100, 20000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.Run("List", func(b *testing.B) {
				s := benchmarkStore(b, count)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = s.List()
				}
			})
			b.Run("UnchangedJSON", func(b *testing.B) {
				s := benchmarkStore(b, count)
				if _, err := s.JSON(""); err != nil {
					b.Fatal(err)
				}
				version := s.instance + ":0"
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := s.JSON(version); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("CompactionPeak", func(b *testing.B) {
				s := benchmarkStore(b, count)
				m, _ := s.Get("message-000000")
				if err := s.Put(Message{ID: "peak-marker", DeviceID: "peer", Direction: "outgoing", Kind: "text", Status: "queued", Created: time.Now(), Revision: 1}); err != nil && count < MaxMessages {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					s.journalRecords = maxJournalRecords
					m.Revision++
					if err := s.Put(m); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("DurableProgress", func(b *testing.B) {
				s := benchmarkStore(b, count)
				m := example()
				if err := s.Put(m); err != nil { // At the cap update an existing identity instead.
					m, _ = s.Get("message-000000")
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
		})
	}
}
