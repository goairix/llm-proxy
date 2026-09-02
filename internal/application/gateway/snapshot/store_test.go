package snapshot

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestStorePublishesWholeSnapshotsConcurrently(t *testing.T) {
	store := NewStore()
	one := newSnapshotForTest(1)
	two := newSnapshotForTest(2)
	store.Publish(one)

	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			current, ok := store.Current()
			if !ok {
				t.Error("current snapshot is unavailable")
				return
			}
			if revision := current.Revision(); revision != 1 && revision != 2 {
				t.Errorf("revision = %d, want 1 or 2", revision)
			}
		}()
	}
	store.Publish(two)
	wait.Wait()

	current, ok := store.Current()
	if !ok || current.Revision() != 2 {
		t.Fatalf("current = %v, ok = %v", current, ok)
	}
}

func TestStoreRejectsNilSnapshot(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Publish(nil) did not panic")
		}
	}()
	NewStore().Publish(nil)
}

func TestStoreRejectsSnapshotNotCreatedByCompiler(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Publish(uncompiled) did not panic")
		}
	}()
	NewStore().Publish(&RuntimeSnapshot{})
}

func TestSessionKeepsSnapshotUsedAtBegin(t *testing.T) {
	store := NewStore()
	store.Publish(newSnapshotForTest(1))
	session, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}

	store.Publish(newSnapshotForTest(2))
	if session.Revision() != 1 {
		t.Fatalf("session revision = %d, want 1", session.Revision())
	}
}

func newSnapshotForTest(revision int64) *RuntimeSnapshot {
	return &RuntimeSnapshot{
		revision:    revision,
		virtualKeys: make(map[[32]byte]AccessContext),
		routes:      make(map[RouteKey]RoutePlan),
		providers:   make(map[uuid.UUID]Provider),
		credentials: make(map[CredentialPoolKey][]CredentialEnvelope),
		compiled:    true,
	}
}
