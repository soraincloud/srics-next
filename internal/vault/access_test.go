package vault

import (
	"context"
	"errors"
	"filippo.io/age"
	"testing"
	"time"
)

func TestAccessIdleExpiryAndIndependentSessions(t *testing.T) {
	key, _ := age.GenerateX25519Identity()
	a := NewAccess(context.Background(), key, 50*time.Millisecond)
	b := NewAccess(context.Background(), key, time.Hour)
	defer b.Lock()
	_, ctx, _, e := a.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	// Reading status does not extend the deadline.
	for i := 0; i < 4; i++ {
		a.Snapshot()
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle key never cleared")
	}
	if _, _, _, e = a.Snapshot(); !errors.Is(e, ErrLocked) {
		t.Fatal("expired session unlocked")
	}
	if a.Touch() {
		t.Fatal("activity resurrected expired key")
	}
	if _, _, _, e = b.Snapshot(); e != nil {
		t.Fatal("one session locked another")
	}
	called := false
	if e = a.Commit(context.Background(), func() error { called = true; return nil }); !errors.Is(e, ErrLocked) || called {
		t.Fatal("commit after lock")
	}
}
