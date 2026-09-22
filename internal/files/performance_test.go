package files

import (
	"fmt"
	"testing"
)

func BenchmarkUploadRequestLookup(b *testing.B) {
	s := &Service{tasks: make(map[string]*uploadTask), requests: make(map[uploadRequestKey]*uploadTask)}
	for i := 0; i < 20000; i++ {
		id := fmt.Sprint(i)
		s.tasks[id] = &uploadTask{diskUpload: diskUpload{Upload: Upload{ID: id}, OwnerID: "peer", RequestID: "request-" + id}}
		s.requests[uploadRequestKey{"peer", "request-" + id}] = s.tasks[id]
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		found := s.requests[uploadRequestKey{"peer", "request-19999"}]
		if found == nil {
			b.Fatal("upload request missing")
		}
	}
}
