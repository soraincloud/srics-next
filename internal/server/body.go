package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

type timedBody struct {
	io.ReadCloser
	mu         sync.Mutex
	ctx        context.Context
	controller *http.ResponseController
	end        time.Time
	idle       time.Duration
}

func (b *timedBody) Read(p []byte) (int, error) {
	if err := b.setDeadline(); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

func (b *timedBody) setDeadline() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(b.idle)
	if b.end.Before(deadline) {
		deadline = b.end
	}
	if err := b.controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (b *timedBody) stopRead() {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.controller.SetReadDeadline(time.Now())
}

// Bound idle and total body time, so a slow sender cannot retain a mutation
// lock forever. Large legacy uploads get a size-dependent budget; resumable
// uploads send 8 MiB chunks and can retry individual chunks after a timeout.
func limitBodyTime(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.Body == http.NoBody {
		return
	}
	budget := 5 * time.Minute
	if r.ContentLength >= 0 {
		budget = time.Minute + time.Duration(min(r.ContentLength/(64<<10), int64((48*time.Hour)/time.Second)))*time.Second
	}
	if r.URL.Path == "/api/auth/login" {
		budget = 15 * time.Second
	}
	body := &timedBody{ReadCloser: r.Body, ctx: r.Context(), controller: http.NewResponseController(w), end: time.Now().Add(budget), idle: 30 * time.Second}
	// Also bound net/http's drain of bodies rejected before the handler reads.
	_ = body.setDeadline()
	r.Body = body
}
