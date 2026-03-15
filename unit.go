package cache

import (
	"fmt"
	"time"
)

// unit is the internal representation of a single cache entry. It stores the
// user-provided data alongside expiration and size metadata.
type unit[T any] struct {
	Data T `json:"data"`

	Exp  time.Duration `json:"exp,omitempty"`
	Size uint64        `json:"size,omitempty"`
}

// revive sets the expiration time to time.Since(runtimeStart) + lt, anchoring
// the expiry to the monotonic clock so it is unaffected by wall-clock adjustments.
// If lt is 0 the unit is left unchanged (no-op).
func (u *unit[T]) revive(lt time.Duration) {
	if lt == 0 {
		return
	}

	u.Exp = time.Since(runtimeStart) + lt
}

// expired reports whether the unit has passed its expiration time using
// the monotonic clock offset from runtimeStart. Units with Exp == 0 never
// expire (they were created without a TTL).
func (u *unit[T]) expired() bool {
	return u.Exp != 0 && time.Since(runtimeStart) >= u.Exp
}

// unitError wraps a cache operation error together with the key that caused it,
// allowing callers to identify which key triggered the error while still using
// [errors.Is] against the sentinel error values.
type unitError[K comparable] struct {
	k   K
	err error
}

func (ue *unitError[K]) Error() string {
	return fmt.Sprintf("key %q: %s", ue.k, ue.err)
}

func (ue *unitError[K]) Unwrap() error { return ue.err }
