package server

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

type session struct {
	expires time.Time
	csrf    string
	vault   *vault.Access
}
type LibraryAPI struct {
	private            privateSecurity
	store              *library.Library
	converter          media.Converter
	ctx                context.Context
	mu                 sync.Mutex
	sessions           map[string]session
	loginMu            sync.Mutex
	failures           int
	nextLogin          time.Time
	backup             backup.Client
	cloud              backup.Client
	backupMu           sync.Mutex
	backupActive       bool
	backupDailyAt      string
	backupStateErrors  map[string]error
	backupActiveTarget string
	backupHistoryMu    sync.Mutex
	backupHistories    map[string]snapshotCache
	wg                 sync.WaitGroup
}

func (s *Server) EnableLibrary(l *library.Library, c media.Converter, b backup.Client) {
	s.library = &LibraryAPI{store: l, converter: c, ctx: s.ctx, sessions: map[string]session{}, backup: b, private: privateSecurity{uploads: make(chan struct{}, 1)}}
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (a *LibraryAPI) current(r *http.Request) (session, bool) {
	c, e := r.Cookie("srics_session")
	if e != nil {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok || time.Now().After(s.expires) {
		s.vault.Lock()
		delete(a.sessions, c.Value)
		return session{}, false
	}
	return s, true
}
func (a *LibraryAPI) newSession(w http.ResponseWriter, r *http.Request) {
	token := randomToken()
	s := session{expires: time.Now().Add(24 * time.Hour), csrf: randomToken()}
	a.mu.Lock()
	for k, v := range a.sessions {
		if time.Now().After(v.expires) {
			v.vault.Lock()
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 32 {
		for k, old := range a.sessions {
			old.vault.Lock()
			delete(a.sessions, k)
			break
		}
	}
	a.sessions[token] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "srics_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(w, 200, map[string]any{"authenticated": true, "configured": true, "csrf": s.csrf})
}
func apiError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		apiError(w, 400, errors.New("请求格式不正确"))
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		apiError(w, 400, errors.New("请求包含多余内容"))
		return false
	}
	return true
}
func (a *LibraryAPI) auth(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/auth" && r.Method == "GET" {
		hash, err := a.store.Setting("password")
		if err != nil {
			apiError(w, 503, err)
			return false
		}
		s, ok := a.current(r)
		wrapped, _ := a.store.Setting("vault-key")
		idle, _ := a.vaultIdle()
		writeJSON(w, 200, map[string]any{"configured": len(hash) > 0, "authenticated": ok, "csrf": s.csrf, "vaultConfigured": len(wrapped) > 0, "vaultIdleMinutes": idle})
		return false
	}
	if r.URL.Path == "/api/auth/setup" {
		apiError(w, http.StatusNotFound, errors.New("请在本机程序中设置登录密码"))
		return false
	}
	if r.URL.Path == "/api/auth/login" {
		if r.Method != "POST" {
			methodNotAllowed(w, "POST")
			return false
		}
		if r.Header.Get("X-SRICS-Request") != "app" {
			apiError(w, 403, errors.New("无效请求"))
			return false
		}
		a.loginMu.Lock()
		defer a.loginMu.Unlock()
		if time.Now().Before(a.nextLogin) {
			w.Header().Set("Retry-After", "5")
			apiError(w, 429, errors.New("尝试过于频繁，请稍后再试"))
			return false
		}
		var body struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &body) {
			return false
		}
		if len(body.Password) > 72 {
			apiError(w, 400, errors.New("密码最长 72 字节"))
			return false
		}
		hash, err := a.store.Setting("password")
		if err != nil {
			apiError(w, 503, err)
			return false
		}
		if len(hash) == 0 || bcrypt.CompareHashAndPassword(hash, []byte(body.Password)) != nil {
			a.failures++
			if a.failures >= 5 {
				a.nextLogin = time.Now().Add(time.Duration(min(a.failures, 60)) * time.Second)
			}
			apiError(w, 401, errors.New("密码不正确"))
			return false
		}
		a.failures = 0
		a.nextLogin = time.Time{}
		a.newSession(w, r)
		return false
	}
	s, ok := a.current(r)
	if !ok {
		apiError(w, 401, errors.New("请先登录"))
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-SRICS-CSRF")), []byte(s.csrf)) != 1 {
		apiError(w, 403, errors.New("会话校验失败，请刷新后重试"))
		return false
	}
	if r.URL.Path == "/api/auth/logout" && r.Method == "POST" {
		c, _ := r.Cookie("srics_session")
		a.mu.Lock()
		s.vault.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "srics_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return false
	}
	if err := a.store.Check(); err != nil {
		apiError(w, 503, err)
		return false
	}
	return true
}
func (a *LibraryAPI) handle(w http.ResponseWriter, r *http.Request) {
	var result any
	var err error
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) >= 2 && parts[1] == "vault":
		a.privateAPI(w, r, parts)
		return
	case len(parts) >= 2 && parts[1] == "novels":
		a.novels(w, r, parts)
		return
	case r.URL.Path == "/api/library" && r.Method == "GET":
		result, err = a.list(r)
	case r.URL.Path == "/api/library/stats" && r.Method == "GET":
		var items []library.Item
		items, err = a.store.Items("all", false)
		counts := map[string]int{}
		var size int64
		for _, it := range items {
			counts[it.Module]++
			for _, p := range it.Pages {
				size += p.Size
			}
		}
		result = map[string]any{"counts": counts, "size": size, "dataPath": a.store.Root}
	case r.URL.Path == "/api/uploads" && r.Method == "GET":
		result, err = a.store.Uploads()
	case r.URL.Path == "/api/uploads" && r.Method == "POST":
		var up library.Upload
		if !decode(w, r, &up) {
			return
		}
		result, err = a.store.CreateUpload(up)
	case len(parts) == 3 && parts[1] == "uploads":
		if r.Method == "GET" {
			result, err = a.store.Upload(parts[2])
		} else if r.Method == "DELETE" {
			err = a.store.Cancel(parts[2])
		} else {
			methodNotAllowed(w, "GET, DELETE")
			return
		}
	case len(parts) == 4 && parts[1] == "uploads" && parts[3] == "finish" && r.Method == "POST":
		result, err = a.store.Finish(parts[2])
	case len(parts) == 5 && parts[1] == "uploads" && parts[3] == "files" && r.Method == "PUT":
		index, e := strconv.Atoi(parts[4])
		if e != nil {
			apiError(w, 400, errors.New("无效文件序号"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, library.MaxFile+1)
		result, err = a.store.Receive(r.Context(), parts[2], index, r.Header.Get("X-File-SHA256"), r.Body, a.converter)
	case len(parts) >= 3 && parts[1] == "items":
		id := parts[2]
		if !library.IDPattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 3 {
			switch r.Method {
			case "GET":
				result, err = a.store.Item(id)
			case "PATCH":
				var b struct {
					Name     string   `json:"name"`
					Tags     []string `json:"tags"`
					Revision int      `json:"revision"`
				}
				if !decode(w, r, &b) {
					return
				}
				err = a.store.Update(id, b.Name, b.Tags, b.Revision)
				if err == nil {
					result, err = a.store.Item(id)
				}
			case "DELETE":
				err = a.store.Trash(id, false)
			default:
				methodNotAllowed(w, "GET, PATCH, DELETE")
				return
			}
		} else if len(parts) == 4 && parts[3] == "restore" && r.Method == "POST" {
			err = a.store.Trash(id, true)
		} else if len(parts) == 4 && parts[3] == "progress" && r.Method == "PUT" {
			var b struct {
				Page int `json:"page"`
			}
			if !decode(w, r, &b) {
				return
			}
			err = a.store.Progress(id, b.Page)
		} else if len(parts) == 4 && parts[3] == "download" && (r.Method == "GET" || r.Method == "HEAD") {
			a.download(w, r, id)
			return
		} else if len(parts) == 5 && parts[3] == "pages" && (r.Method == "GET" || r.Method == "HEAD") {
			a.page(w, r, id, parts[4])
			return
		} else {
			http.NotFound(w, r)
			return
		}
	case r.URL.Path == "/api/download" && r.Method == "GET":
		a.downloadBatch(w, r)
		return
	case r.URL.Path == "/api/backup/snapshots" && r.Method == "GET":
		target := r.URL.Query().Get("target")
		if target != "local" && target != "cloud" {
			apiError(w, 400, errors.New("无效备份目标"))
			return
		}
		result, err = a.backupHistory(r.Context(), target, r.URL.Query().Get("after"))
	case r.URL.Path == "/api/backup" && r.Method == "GET":
		target := r.URL.Query().Get("target")
		if target != "" && target != "local" && target != "cloud" {
			apiError(w, 400, errors.New("无效备份目标"))
			return
		}
		result, err = a.backupStatus(target)
	case r.URL.Path == "/api/backup" && r.Method == "POST":
		target := r.URL.Query().Get("target")
		if target != "" && target != "local" && target != "cloud" {
			apiError(w, 400, errors.New("无效备份目标"))
			return
		}
		err = a.startBackup(target)
		if err == nil {
			writeJSON(w, 202, map[string]bool{"running": true})
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		code := 400
		if errors.Is(err, library.ErrMissing) {
			code = 404
		}
		if errors.Is(err, library.ErrConflict) {
			code = 409
		}
		apiError(w, code, err)
		return
	}
	if result == nil {
		result = map[string]bool{"ok": true}
	}
	writeJSON(w, 200, result)
}
func (a *LibraryAPI) list(r *http.Request) (any, error) {
	q := r.URL.Query()
	module := q.Get("module")
	trash := q.Get("trash") == "1"
	if module == "all" && !trash {
		return nil, errors.New("请选择一个资料空间")
	}
	items, err := a.store.Items(module, trash)
	if err != nil {
		return nil, err
	}
	snapshot, _ := strconv.ParseInt(q.Get("snapshot"), 10, 64)
	if snapshot <= 0 {
		for _, it := range items {
			snapshot = max(snapshot, it.Seq)
		}
	}
	seed := q.Get("seed")
	random := q.Get("random") == "1" && module == "images"
	if random && seed == "" {
		seed = randomToken()
	}
	if len(seed) > 128 {
		return nil, errors.New("无效随机轮次")
	}
	name := strings.ToLower(strings.TrimSpace(q.Get("q")))
	tags := q["tag"]
	filtered := []library.Item{}
	allTags := map[string]bool{}
	for _, it := range items {
		for _, t := range it.Tags {
			allTags[t] = true
		}
		if it.Seq > snapshot {
			continue
		}
		if module == "comics" || module == "novels" {
			if !strings.Contains(strings.ToLower(it.Name), name) {
				continue
			}
			matches := true
			for _, t := range tags {
				found := false
				for _, v := range it.Tags {
					if v == t {
						found = true
					}
				}
				matches = matches && found
			}
			if !matches {
				continue
			}
		}
		filtered = append(filtered, it)
	}
	key := func(it library.Item) string {
		if random {
			sum := sha256.Sum256([]byte(seed + it.ID))
			return hex.EncodeToString(sum[:]) + it.ID
		}
		return fmt.Sprintf("%020d", int64(1<<62)-it.Seq)
	}
	sort.Slice(filtered, func(i, j int) bool { return key(filtered[i]) < key(filtered[j]) })
	total := len(filtered)
	after := q.Get("after")
	page := []library.Item{}
	more := false
	for _, it := range filtered {
		if after != "" && key(it) <= after {
			continue
		}
		if len(page) == 40 {
			more = true
			break
		}
		page = append(page, it)
	}
	next := ""
	if more {
		next = key(page[len(page)-1])
	}
	availableTags := []string{}
	for t := range allTags {
		availableTags = append(availableTags, t)
	}
	sort.Strings(availableTags)
	return map[string]any{"items": page, "total": total, "next": next, "snapshot": snapshot, "seed": seed, "tags": availableTags}, nil
}
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune("/\\:*?\"<>|", r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "download"
	}
	return name
}
func attachment(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeName(name)}))
}
func (a *LibraryAPI) page(w http.ResponseWriter, r *http.Request, id, n string) {
	it, err := a.store.Item(id)
	i, e := strconv.Atoi(n)
	if err != nil || e != nil || i < 0 || i >= len(it.Pages) || it.Deleted != "" {
		http.NotFound(w, r)
		return
	}
	p := it.Pages[i]
	obj := p.Object
	kind := p.MIME
	if r.URL.Query().Get("thumb") == "1" && p.Thumb != "" {
		obj = p.Thumb
		kind = "image/jpeg"
	}
	if kind != "image/png" && kind != "image/jpeg" && kind != "image/webp" && kind != "image/gif" {
		apiError(w, 415, errors.New("此格式请下载原件查看"))
		return
	}
	f, err := os.Open(a.store.ObjectPath(obj))
	if err != nil {
		apiError(w, 503, errors.New("原件暂时无法读取"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		apiError(w, 503, err)
		return
	}
	w.Header().Set("Content-Type", kind)
	http.ServeContent(w, r, p.Name, st.ModTime(), f)
}
func (a *LibraryAPI) download(w http.ResponseWriter, r *http.Request, id string) {
	it, err := a.store.Item(id)
	if err != nil || it.Deleted != "" {
		http.NotFound(w, r)
		return
	}
	if it.Module == "comics" {
		a.zip(w, r, []library.Item{it}, safeName(it.Name)+".zip")
		return
	}
	if it.Module == "novels" {
		a.downloadNovel(w, r, id)
		return
	}
	p := it.Pages[0]
	f, err := os.Open(a.store.ObjectPath(p.Object))
	if err != nil {
		apiError(w, 503, errors.New("原件暂时无法读取"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		apiError(w, 503, err)
		return
	}
	attachment(w, p.Name)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, p.Name, st.ModTime(), f)
}
func (a *LibraryAPI) downloadBatch(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 || len(ids) > 100 {
		apiError(w, 400, errors.New("每次请选择 1–100 项"))
		return
	}
	items := []library.Item{}
	module := ""
	for _, id := range ids {
		it, err := a.store.Item(id)
		if err != nil || it.Deleted != "" || (it.Module != "images" && it.Module != "photos") || (module != "" && it.Module != module) {
			apiError(w, 400, errors.New("请选择同一空间内的有效图片"))
			return
		}
		module = it.Module
		items = append(items, it)
	}
	a.zip(w, r, items, "SRICS-图片.zip")
}
func (a *LibraryAPI) zip(w http.ResponseWriter, r *http.Request, items []library.Item, name string) {
	// Check all references before sending headers; streamed archives never create
	// a second full copy on disk. A midstream I/O failure terminates the response.
	for _, it := range items {
		for _, p := range it.Pages {
			info, err := os.Stat(a.store.ObjectPath(p.Object))
			if err != nil || info.Size() != p.Size {
				apiError(w, 503, errors.New("文件缺失或大小异常，请检查资料盘"))
				return
			}
		}
	}
	attachment(w, name)
	w.Header().Set("Content-Type", "application/zip")
	if r.Method == "HEAD" {
		return
	}
	z := zip.NewWriter(w)
	used := map[string]bool{}
	for _, it := range items {
		for i, p := range it.Pages {
			entry := safeName(p.Name)
			if it.Module == "comics" {
				entry = safeName(it.Name) + fmt.Sprintf("/%05d.webp", i+1)
			} else {
				base := entry
				for suffix := 2; used[entry]; suffix++ {
					entry = fmt.Sprintf("%d-%s", suffix, base)
				}
			}
			used[entry] = true
			h := &zip.FileHeader{Name: entry, Method: zip.Store}
			h.SetMode(0600)
			out, err := z.CreateHeader(h)
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			f, err := os.Open(a.store.ObjectPath(p.Object))
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			_, err = io.Copy(out, f)
			f.Close()
			if err != nil || r.Context().Err() != nil {
				panic(http.ErrAbortHandler)
			}
		}
	}
	if z.Close() != nil {
		panic(http.ErrAbortHandler)
	}
}
