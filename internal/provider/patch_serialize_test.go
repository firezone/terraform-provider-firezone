package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	firezone "github.com/firezone/firezone-go"
)

func conflictErr() error {
	return &firezone.APIError{StatusCode: 409, Title: "Conflict"}
}

// The reason serializedPatch exists: concurrent calls for one parent
// must never overlap, because the API's PATCH is read-modify-write and
// the loser of a race gets a 409.
func TestSerializedPatchSerializesPerParent(t *testing.T) {
	t.Parallel()

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = serializedPatch(context.Background(), t.Name(), func() error {
				n := atomic.AddInt32(&inFlight, 1)
				for {
					max := atomic.LoadInt32(&maxInFlight)
					if n <= max || atomic.CompareAndSwapInt32(&maxInFlight, max, n) {
						break
					}
				}
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Fatalf("observed %d concurrent patches for one parent, want 1", maxInFlight)
	}
}

// Different parents are independent, so locking one must not block the
// other - otherwise a large config serializes entirely.
func TestSerializedPatchDoesNotSerializeAcrossParents(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = serializedPatch(context.Background(), t.Name()+"-a", func() error {
			close(entered)
			<-release
			return nil
		})
	}()

	<-entered
	if err := serializedPatch(context.Background(), t.Name()+"-b", func() error { return nil }); err != nil {
		t.Fatalf("patch on second parent: %v", err)
	}
	close(release)
	<-done
}

func TestSerializedPatchRetriesConflict(t *testing.T) {
	t.Parallel()

	calls := 0
	err := serializedPatch(context.Background(), t.Name(), func() error {
		calls++
		if calls < 3 {
			return conflictErr()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

// A conflict that never clears has to surface, not spin forever.
func TestSerializedPatchGivesUpOnPersistentConflict(t *testing.T) {
	t.Parallel()

	calls := 0
	err := serializedPatch(context.Background(), t.Name(), func() error {
		calls++
		return conflictErr()
	})
	if !firezone.IsConflict(err) {
		t.Fatalf("expected the conflict to be returned, got %v", err)
	}
	if calls != patchRetries+1 {
		t.Fatalf("expected %d attempts, got %d", patchRetries+1, calls)
	}
}

// Non-409 errors are the caller's to handle and must come back
// immediately - retrying a 422 just delays the same failure.
func TestSerializedPatchDoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	calls := 0
	err := serializedPatch(context.Background(), t.Name(), func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected the original error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt, got %d", calls)
	}
}

func TestSerializedPatchStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := serializedPatch(ctx, t.Name(), func() error {
		calls++
		cancel()
		return conflictErr()
	})
	if !firezone.IsConflict(err) {
		t.Fatalf("expected the conflict to be returned, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt before cancellation, got %d", calls)
	}
}
