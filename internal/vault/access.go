package vault

import (
	"context"
	"io"
	"sync"
	"time"

	"filippo.io/age"
)

// Access owns one login session's key and cancellation lifetime. File I/O never
// holds its mutex; only publishing metadata is serialized with Lock.
type Access struct {
	mu      sync.Mutex
	key     *age.X25519Identity
	ctx     context.Context
	cancel  context.CancelFunc
	timer   *time.Timer
	expires time.Time
	idle    time.Duration
}

func NewAccess(parent context.Context, key *age.X25519Identity, idle time.Duration) *Access {
	ctx, cancel := context.WithCancel(parent)
	a := &Access{key: key, ctx: ctx, cancel: cancel, idle: idle}
	a.Touch()
	context.AfterFunc(ctx, a.Lock)
	return a
}
func (a *Access) Lock() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.key = nil
	a.cancel()
	if a.timer != nil {
		a.timer.Stop()
	}
}
func (a *Access) valid() bool {
	return a.key != nil && a.ctx.Err() == nil && time.Now().Before(a.expires)
}
func (a *Access) Touch() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key == nil || a.ctx.Err() != nil || (!a.expires.IsZero() && !time.Now().Before(a.expires)) {
		return false
	}
	a.expires = time.Now().Add(a.idle)
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(a.idle, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if !time.Now().Before(a.expires) {
			a.key = nil
			a.cancel()
		}
	})
	return true
}
func (a *Access) Snapshot() (*age.X25519Identity, context.Context, time.Time, error) {
	if a == nil {
		return nil, nil, time.Time{}, ErrLocked
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.valid() {
		return nil, nil, time.Time{}, ErrLocked
	}
	return a.key, a.ctx, a.expires, nil
}
func (a *Access) Commit(ctx context.Context, fn func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.valid() {
		return ErrLocked
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

type ContextReader struct {
	Ctx    context.Context
	Reader io.Reader
}

func (r ContextReader) Read(p []byte) (int, error) {
	if err := r.Ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.Reader.Read(p)
	if e := r.Ctx.Err(); e != nil {
		clear(p[:n])
		return 0, e
	}
	return n, err
}

type ContextWriter struct {
	Ctx    context.Context
	Writer io.Writer
}

func (w ContextWriter) Write(p []byte) (int, error) {
	if err := w.Ctx.Err(); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}
