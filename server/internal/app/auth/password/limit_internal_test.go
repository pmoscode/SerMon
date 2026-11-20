package password

import (
	"testing"
	"time"
)

// Each check needs Params.Memory; the hasher runs at most MaxHashMemory worth of them at once, so
// a flood of logins can't exhaust the server's memory (M1-35).
func TestHasher_BoundsTheMemoryOfParallelChecks(t *testing.T) {
	h, err := NewHasher(Params{Memory: 64 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	if want := MaxHashMemory / (64 << 20); cap(h.slots) != want {
		t.Fatalf("slots = %d, want %d", cap(h.slots), want)
	}
	small, _ := NewHasher(Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	if cap(small.slots) != maxSlots {
		t.Errorf("small hashes: %d slots, want the cap %d", cap(small.slots), maxSlots)
	}

	for range cap(h.slots) {
		h.slots <- struct{}{} // all slots busy
	}
	done := make(chan struct{})
	go func() {
		h.VerifyDummy("password")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a check ran although all slots were busy")
	case <-time.After(200 * time.Millisecond):
	}
	<-h.slots
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the check did not run after a slot was freed")
	}
}
