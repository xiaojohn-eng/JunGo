package log

import (
	"sync"
	"testing"
)

// JunGo regression: restarting the core updates the log level while old proxy
// adapters may log from finalizers on another goroutine.
func TestConcurrentLevelChangesAndLogging(t *testing.T) {
	previous := Level()
	defer SetLevel(previous)
	SetLevel(INFO)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(writer bool) {
			defer workers.Done()
			for i := 0; i < 10000; i++ {
				if writer {
					SetLevel(INFO + LogLevel(i%2))
				} else {
					_ = Level()
					print(Event{LogLevel: DEBUG, Payload: "concurrent old proxy cleanup"})
				}
			}
		}(worker%2 == 0)
	}
	workers.Wait()
}
