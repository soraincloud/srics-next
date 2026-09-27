package server

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
)

type privateSecurity struct {
	mu       sync.Mutex
	failures int
	next     time.Time
	uploads  chan struct{}
}

func (a *LibraryAPI) vaultStatus(r *http.Request) (any, error) {
	wrapped, err := a.store.Setting("vault-key")
	if err != nil {
		return nil, err
	}
	idle, err := a.vaultIdle()
	if err != nil {
		return nil, err
	}
	s, _ := a.current(r)
	_, _, expires, e := s.vault.Snapshot()
	return map[string]any{"configured": len(wrapped) > 0, "unlocked": e == nil, "expires": expires, "idleMinutes": idle}, nil
}
func (a *LibraryAPI) vaultIdle() (int, error) {
	b, err := a.store.Setting("vault-idle")
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(string(b))
	if n < 1 || n > 60 {
		n = 10
	}
	return n, nil
}
func (a *LibraryAPI) unlockVault(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &b) {
		return
	}
	if len(b.Password) > 1024 {
		privateError(w, errors.New("保险库口令过长"))
		return
	}
	a.private.mu.Lock()
	defer a.private.mu.Unlock()
	if time.Now().Before(a.private.next) {
		w.Header().Set("Retry-After", "5")
		apiError(w, 429, errors.New("尝试过于频繁，请稍后再试"))
		return
	}
	wrapped, err := a.store.Setting("vault-key")
	if err != nil {
		privateError(w, err)
		return
	}
	if len(wrapped) == 0 {
		apiError(w, 409, errors.New("请先在本机配置窗口设置保险库口令"))
		return
	}
	key, err := vault.Unlock(wrapped, b.Password)
	b.Password = ""
	if err != nil {
		a.private.failures++
		if a.private.failures >= 5 {
			a.private.next = time.Now().Add(time.Duration(min(a.private.failures, 60)) * time.Second)
		}
		apiError(w, 403, errors.New("保险库口令不正确"))
		return
	}
	idle, err := a.vaultIdle()
	if err != nil {
		privateError(w, err)
		return
	}
	a.private.failures = 0
	a.private.next = time.Time{}
	cookie, _ := r.Cookie("srics_session")
	a.mu.Lock()
	s, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(s.expires) || r.Context().Err() != nil {
		a.mu.Unlock()
		apiError(w, 401, errors.New("请重新登录"))
		return
	}
	s.vault.Lock()
	s.vault = vault.NewAccess(a.ctx, key, time.Duration(idle)*time.Minute)
	a.sessions[cookie.Value] = s
	a.mu.Unlock()
	result, err := a.vaultStatus(r)
	if err != nil {
		privateError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func privateError(w http.ResponseWriter, err error) {
	code := 400
	if errors.Is(err, vault.ErrLocked) || errors.Is(err, context.Canceled) {
		code = 423
		err = errors.New("保险库已锁定，请重新解锁")
	}
	if errors.Is(err, library.ErrMissing) {
		code = 404
	}
	if errors.Is(err, library.ErrConflict) {
		code = 409
	}
	apiError(w, code, err)
}
func (a *LibraryAPI) privateAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.URL.Path == "/api/vault/status" && r.Method == "GET" {
		result, err := a.vaultStatus(r)
		if err != nil {
			privateError(w, err)
		} else {
			writeJSON(w, 200, result)
		}
		return
	}
	if r.URL.Path == "/api/vault/unlock" && r.Method == "POST" {
		a.unlockVault(w, r)
		return
	}
	s, _ := a.current(r)
	if r.URL.Path == "/api/vault/lock" && r.Method == "POST" {
		s.vault.Lock()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	_, leaseCtx, _, err := s.vault.Snapshot()
	if err != nil {
		privateError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	body := r.Body
	if timed, ok := body.(*timedBody); ok {
		timed.ctx = ctx
	}
	response := http.NewResponseController(w)
	done := make(chan struct{})
	stopped := context.AfterFunc(leaseCtx, func() {
		defer close(done)
		cancel()
		if timed, ok := body.(*timedBody); ok {
			timed.stopRead()
		} else {
			_ = response.SetReadDeadline(time.Now())
		}
		_ = response.SetWriteDeadline(time.Now())
		body.Close()
	})
	defer func() {
		if !stopped() {
			<-done
		}
	}()
	r = r.WithContext(ctx)
	var result any
	switch {
	case len(parts) >= 3 && parts[2] == "transfers":
		a.transfers(w, r, s.vault, transferParts(r.URL.Path, "/api/vault/transfers"))
		return
	case r.URL.Path == "/api/vault/activity" && r.Method == "POST":
		if !s.vault.Touch() {
			privateError(w, vault.ErrLocked)
			return
		}
		result, err = a.vaultStatus(r)
	case r.URL.Path == "/api/vault/list" && r.Method == "POST":
		var q privateQuery
		if !decode(w, r, &q) {
			return
		}
		result, err = a.privateList(ctx, s.vault, q)
	case r.URL.Path == "/api/vault/download" && r.Method == "GET":
		a.privateDownload(w, r, s.vault, r.URL.Query()["id"], true, false)
		return
	case len(parts) == 4 && parts[2] == "items" && library.IDPattern.MatchString(parts[3]):
		switch r.Method {
		case "PUT":
			select {
			case a.private.uploads <- struct{}{}:
				defer func() { <-a.private.uploads }()
			default:
				apiError(w, 429, errors.New("另一项私密上传正在处理，请稍后重试"))
				return
			}
			name, e := url.PathUnescape(r.Header.Get("X-File-Name"))
			if e != nil {
				privateError(w, e)
				return
			}
			module := r.Header.Get("X-File-Module")
			size, e := strconv.ParseInt(r.Header.Get("X-File-Size"), 10, 64)
			if e != nil {
				privateError(w, errors.New("无效文件大小"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, library.MaxPrivateFile+1)
			result, err = a.store.ReceivePrivate(ctx, s.vault, parts[3], module, name, size, r.Body)
		case "PATCH":
			var b struct {
				Name     string `json:"name"`
				Action   string `json:"action"`
				Revision int    `json:"revision"`
			}
			if !decode(w, r, &b) {
				return
			}
			if b.Action == "purge" {
				err = a.store.PurgePrivate(ctx, s.vault, parts[3], b.Revision)
			} else {
				result, err = a.store.ChangePrivate(ctx, s.vault, parts[3], b.Name, b.Action, b.Revision)
			}
		default:
			methodNotAllowed(w, "PUT, PATCH")
			return
		}
	case len(parts) == 5 && parts[2] == "items" && library.IDPattern.MatchString(parts[3]) && r.Method == "GET" && (parts[4] == "download" || parts[4] == "preview"):
		a.privateDownload(w, r, s.vault, []string{parts[3]}, false, parts[4] == "preview")
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err == nil {
		_, _, _, err = s.vault.Snapshot()
	}
	if err != nil {
		privateError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

type privateQuery struct {
	Module   string `json:"module"`
	Query    string `json:"q"`
	Trash    bool   `json:"trash"`
	Random   bool   `json:"random"`
	Seed     string `json:"seed"`
	Snapshot int64  `json:"snapshot"`
	After    string `json:"after"`
}

func (a *LibraryAPI) privateList(ctx context.Context, access *vault.Access, q privateQuery) (any, error) {
	if q.Module != "files" && q.Module != "private" {
		return nil, errors.New("请选择私密空间")
	}
	if len(q.Query) > 1024 || len(q.Seed) > 128 || len(q.After) > 128 {
		return nil, errors.New("搜索条件过长")
	}
	if err := a.store.ExpirePrivateTrash(ctx, access, a.trashDays); err != nil {
		return nil, err
	}
	items, err := a.store.PrivateItems(ctx, access)
	if err != nil {
		return nil, err
	}
	if q.Snapshot <= 0 {
		for _, it := range items {
			q.Snapshot = max(q.Snapshot, it.Seq)
		}
	}
	if q.Random && q.Module == "private" && q.Seed == "" {
		q.Seed = randomToken()
	}
	order := func(it library.PrivateItem) string {
		if q.Random && q.Module == "private" {
			h := sha256.Sum256([]byte(q.Seed + it.ID))
			return hex.EncodeToString(h[:])
		}
		return fmt.Sprintf("%020d", int64(^uint64(0)>>1)-it.Seq)
	}
	found := []library.PrivateItem{}
	for _, it := range items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if it.Module == q.Module && (it.Deleted != "") == q.Trash && it.Seq <= q.Snapshot && (q.Module != "files" || strings.Contains(strings.ToLower(it.Name), strings.ToLower(strings.TrimSpace(q.Query)))) {
			found = append(found, it)
		}
	}
	sort.Slice(found, func(i, j int) bool { return order(found[i]) < order(found[j]) })
	total := len(found)
	page := []library.PrivateItem{}
	for _, it := range found {
		if order(it) > q.After {
			page = append(page, it)
			if len(page) == 41 {
				break
			}
		}
	}
	next := ""
	if len(page) > 40 {
		page = page[:40]
		next = order(page[39])
	}
	return map[string]any{"items": page, "total": total, "next": next, "snapshot": q.Snapshot, "seed": q.Seed}, nil
}
func (a *LibraryAPI) privateDownload(w http.ResponseWriter, r *http.Request, access *vault.Access, ids []string, batch, thumb bool) {
	if len(ids) < 1 || len(ids) > 100 {
		privateError(w, errors.New("每次请选择 1–100 项"))
		return
	}
	type source struct {
		item   library.PrivateItem
		reader io.ReadCloser
		size   int64
	}
	sources := []source{}
	defer func() {
		for _, s := range sources {
			s.reader.Close()
		}
	}()
	for _, id := range ids {
		it, err := a.store.PrivateItem(access, id)
		if err != nil {
			privateError(w, err)
			return
		}
		if it.Deleted != "" || (thumb && it.Module != "private") || (len(sources) > 0 && it.Module != sources[0].item.Module) {
			privateError(w, library.ErrMissing)
			return
		}
		f, n, err := a.store.PrivateRead(r.Context(), access, it, thumb)
		if err != nil {
			privateError(w, err)
			return
		}
		sources = append(sources, source{it, f, n})
	}
	if _, _, _, err := access.Snapshot(); err != nil {
		privateError(w, err)
		return
	}
	out := vault.ContextWriter{Ctx: r.Context(), Writer: w}
	if !batch {
		s := sources[0]
		if thumb {
			w.Header().Set("Content-Type", "image/png")
		} else {
			attachment(w, s.item.Original)
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		// Do not use Content-Length: the last age chunk must be authenticated before
		// successful EOF. An abort must remain a failed download even if all expected
		// plaintext bytes happened to be sent before a late storage failure.
		if _, err := io.Copy(out, s.reader); err != nil || r.Context().Err() != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	attachment(w, "SRICS-私密文件.zip")
	w.Header().Set("Content-Type", "application/zip")
	z := zip.NewWriter(out)
	used := map[string]bool{}
	for _, s := range sources {
		name := safeName(s.item.Original)
		base := name
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%d-%s", i, base)
		}
		used[name] = true
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		h.SetMode(0600)
		entry, err := z.CreateHeader(h)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		if _, err = io.Copy(entry, s.reader); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
	if r.Context().Err() != nil || z.Close() != nil {
		panic(http.ErrAbortHandler)
	}
}
