package cortex

import "testing"

// Regression: Stop() must deterministically stop the memory-maintenance
// goroutine. The goroutine used to read the shared field `m.memoryMaintenanceStop`
// directly inside its select, racing with Stop()'s `m.memoryMaintenanceStop = nil`.
// Under `go test -race` that surfaced as:
//
//	Write at ... Stop() ... integration.go
//	Previous read at ... startMemoryMaintenance.func1() ... integration.go
//
// and, depending on timing, left the goroutine blocked forever on a nil channel
// so Stop's 3s grace expired without the loop actually exiting.
//
// The fix hands the goroutine a local copy of the stop channel. This test locks
// the contract in two ways: the done channel closes (goroutine exited) before
// Stop returns, and Stop is idempotent. Repeat a few times so CI's -race has
// several chances to observe the write/read pair if it ever regresses.
//
// It intentionally avoids Start() (which would open the shared on-disk memory
// store) and drives only the maintenance loop, so it stays hermetic. The manager
// is built with Enabled=false — that path returns an empty shell without loading
// any subsystem — which keeps each iteration cheap.
func TestManagerStopHaltsMaintenanceLoop(t *testing.T) {
	for i := 0; i < 5; i++ {
		mgr := NewManagerWithProfileAndConfig(t.TempDir(), nil, "", &ManagerConfig{Enabled: false})
		mgr.startMemoryMaintenance()

		done := mgr.memoryMaintenanceDone
		if mgr.memoryMaintenanceStop == nil || done == nil {
			t.Fatal("maintenance loop did not start")
		}

		mgr.Stop()

		select {
		case <-done:
			// good: the goroutine has exited by the time Stop returns
		default:
			t.Fatal("Stop returned while the maintenance goroutine was still running")
		}
		if mgr.memoryMaintenanceStop != nil {
			t.Fatal("Stop left the stop channel set on the manager")
		}

		// Idempotent: a second Stop on an already-stopped manager must be a no-op.
		mgr.Stop()
	}
}
