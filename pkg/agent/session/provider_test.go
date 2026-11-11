package session

import (
	"testing"

	"github.com/google/uuid"
)

// sessionSeed must be stable per session (a resume reproduces the same stream)
// and non-negative on every platform (the provider takes a signed integer). Two
// different sessions should almost never share a seed.
func TestSessionSeedIsStableAndNonNegative(t *testing.T) {
	id := uuid.MustParse("01a1cbe3-7b46-762f-b3a8-5dc29c59f971")
	first := sessionSeed(id)
	if again := sessionSeed(id); again != first {
		t.Fatalf("sessionSeed(%s) = %d then %d, want a stable value", id, first, again)
	}
	if first < 0 {
		t.Fatalf("sessionSeed(%s) = %d, want a non-negative seed", id, first)
	}

	// The hash, not the id's leading bytes, decides the seed: two ids that
	// differ only in a trailing byte must still land on different seeds.
	a := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	b := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	if sessionSeed(a) == sessionSeed(b) {
		t.Fatalf("ids differing only at the tail share a seed (%d)", sessionSeed(a))
	}
}
