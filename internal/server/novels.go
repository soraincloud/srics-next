package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/soraincloud/srics-next/internal/library"
)

func (a *LibraryAPI) novels(w http.ResponseWriter, r *http.Request, p []string) {
	var result any
	var err error
	if len(p) > 2 && !library.IDPattern.MatchString(p[2]) {
		http.NotFound(w, r)
		return
	}
	if len(p) > 4 && p[3] == "chapters" && !library.IDPattern.MatchString(p[4]) {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(p) == 2 && r.Method == "POST":
		var b struct {
			ID   string   `json:"id"`
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if !decode(w, r, &b) {
			return
		}
		result, err = a.store.CreateNovel(b.ID, b.Name, b.Tags)
	case len(p) == 3 && r.Method == "GET":
		result, err = a.store.Novel(p[2])
	case len(p) == 4 && p[3] == "status" && r.Method == "PUT":
		var b struct {
			Completed *bool `json:"completed"`
			Revision  int   `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		if b.Completed == nil || b.Revision < 1 {
			err = errors.New("请提供完结状态和当前小说版本")
		} else {
			result, err = a.store.SetNovelCompleted(p[2], *b.Completed, b.Revision)
		}
	case len(p) == 4 && p[3] == "chapters" && r.Method == "POST":
		var b struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Body     string `json:"body"`
			Revision int    `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		result, err = a.store.CreateChapter(p[2], b.ID, b.Title, b.Body, b.Revision)
	case len(p) == 4 && p[3] == "order" && r.Method == "PUT":
		var b struct {
			IDs      []string `json:"ids"`
			Revision int      `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		err = a.store.ReorderChapters(p[2], b.IDs, b.Revision)
	case len(p) == 4 && p[3] == "progress" && r.Method == "PUT":
		var b library.NovelBookmark
		if !decode(w, r, &b) {
			return
		}
		err = a.store.SaveNovelBookmark(p[2], b)
	case len(p) == 5 && p[3] == "chapters" && r.Method == "GET":
		result, err = a.store.Chapter(p[2], p[4])
	case len(p) == 5 && p[3] == "chapters" && r.Method == "PUT":
		var b struct {
			Title     string `json:"title"`
			Body      string `json:"body"`
			Revision  int    `json:"revision"`
			Automatic bool   `json:"automatic"`
		}
		if !decode(w, r, &b) {
			return
		}
		result, err = a.store.SaveChapter(p[2], p[4], b.Title, b.Body, b.Revision, b.Automatic)
	case len(p) == 5 && p[3] == "chapters" && r.Method == "DELETE",
		len(p) == 6 && p[3] == "chapters" && p[5] == "restore" && r.Method == "POST":
		var b struct {
			Revision int `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		err = a.store.TrashChapter(p[2], p[4], b.Revision, len(p) == 6)
	case len(p) == 6 && p[3] == "chapters" && p[5] == "purge" && r.Method == "POST":
		var b struct {
			Revision int `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		err = a.store.PurgeChapter(p[2], p[4], b.Revision)
	case len(p) == 6 && p[3] == "chapters" && p[5] == "versions" && r.Method == "GET":
		result, err = a.store.Versions(p[2], p[4])
	case len(p) == 7 && p[3] == "chapters" && p[5] == "versions" && r.Method == "GET":
		v, e := strconv.Atoi(p[6])
		if e != nil {
			err = errors.New("无效版本")
		} else {
			result, err = a.store.Version(p[2], p[4], v)
		}
	case len(p) == 7 && p[3] == "chapters" && p[5] == "versions" && r.Method == "POST":
		var b struct {
			Revision int `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		v, e := strconv.Atoi(p[6])
		if e != nil {
			err = errors.New("无效版本")
		} else {
			result, err = a.store.RestoreVersion(p[2], p[4], b.Revision, v)
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
func (a *LibraryAPI) downloadNovel(w http.ResponseWriter, r *http.Request, id string) {
	f, err := os.CreateTemp(filepath.Join(a.store.Root, "staging"), ".pending-novel-export-")
	if err != nil {
		apiError(w, 503, errors.New("导出暂不可用，请检查资料盘"))
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	name, err := a.store.ExportNovel(id, f)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	if _, err = f.Seek(0, 0); err != nil {
		apiError(w, 503, err)
		return
	}
	info, err := f.Stat()
	if err != nil {
		apiError(w, 503, err)
		return
	}
	attachment(w, name+".txt")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
