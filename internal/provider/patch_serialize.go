package provider

import (
	"context"
	"math/rand"
	"sync"
	"time"

	firezone "github.com/firezone/firezone-sdk-go"
)

// The membership PATCH endpoint used to be read-modify-write on the
// Group row: the API loaded the Group with its memberships, computed the
// new member set in application code, and wrote the whole association
// back. Two PATCHes against one Group that overlapped raced, and the
// loser failed with HTTP 409 - an Ecto stale-entry crash surfacing as a
// status code rather than a designed response.
//
// Terraform makes that overlap the normal case rather than a rare one:
// every firezone_group_membership for one Group is an independent
// resource, and the default -parallelism=10 applies or destroys ten of
// them at once.
//
// The portal has since been fixed to write membership rows directly -
// delete_all for removals, insert_all with on_conflict: :nothing for
// additions, inside one transaction - which makes concurrent PATCHes
// safe and the endpoint genuinely idempotent. This serialization stays
// anyway, because the provider has to work against portals that predate
// that fix: self-hosted deployments, and any cloud environment not yet
// on a build that includes it. It is cheap insurance - one mutex per
// Group, with different Groups still running in parallel.
//
// firezone_pool_member uses this too, for symmetry and for the same
// out-of-process races. Its endpoint was never affected by the
// stale-entry bug: PoolMemberController already wrote rows directly.
//
// Neither half belongs in firezone-sdk-go: this is a Terraform-shaped
// concurrency problem, and an SDK that silently serialized callers'
// requests would surprise everyone else.

// parentLocks holds one mutex per parent object ID (Group ID, pool
// Resource ID). Entries are never evicted - one mutex per object
// touched in a run is bounded by config size, and a run is short-lived.
var parentLocks sync.Map

// patchRetries is how many times serializedPatch re-attempts after a
// 409. Against a fixed portal this never fires. Against an older one it
// covers the conflicts the mutex cannot - another Terraform run, the
// admin portal, a directory sync - and a small budget is enough for
// those; a config racing something that holds the row indefinitely
// should fail rather than spin.
const patchRetries = 4

// serializedPatch runs patch while holding the lock for parentID,
// retrying on HTTP 409 with jittered backoff.
//
// patch must be idempotent, which the add/remove PATCH shape is, and is
// now documented to be: the server computes a set, so re-adding a member
// already added or removing one already removed converges on the same
// state rather than erroring. That's what makes retrying safe without
// first re-reading.
func serializedPatch(ctx context.Context, parentID string, patch func() error) error {
	value, _ := parentLocks.LoadOrStore(parentID, &sync.Mutex{})
	mu := value.(*sync.Mutex)

	mu.Lock()
	defer mu.Unlock()

	var err error
	for attempt := 0; ; attempt++ {
		if err = patch(); err == nil || !firezone.IsConflict(err) {
			return err
		}
		if attempt == patchRetries {
			return err
		}

		// 50ms, 100ms, 200ms, 400ms, each with up to 50% jitter so
		// several racing processes don't retry in lockstep.
		backoff := time.Duration(50<<attempt) * time.Millisecond
		backoff += time.Duration(rand.Int63n(int64(backoff / 2)))

		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
	}
}
