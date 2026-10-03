package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
)

func (a *LibraryAPI) documents(w http.ResponseWriter, r *http.Request, p []string) {
	var result library.Document
	var err error
	if len(p) > 2 && !library.IDPattern.MatchString(p[2]) {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(p) == 3 && r.Method == "GET":
		result, err = a.store.Document(r.Context(), p[2])
	case len(p) == 2 && r.Method == "POST", len(p) == 3 && r.Method == "PUT":
		var b struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Tags     []string `json:"tags"`
			Body     string   `json:"body"`
			Revision int      `json:"revision"`
		}
		if !decode(w, r, &b) {
			return
		}
		if r.Method == "PUT" {
			if b.ID != "" && b.ID != p[2] || b.Revision < 1 {
				apiError(w, 400, errors.New("无效文档编号或版本"))
				return
			}
			b.ID = p[2]
		} else if b.Revision != 0 {
			apiError(w, 400, errors.New("新文档不能指定已有版本"))
			return
		}
		result, err = a.store.SaveDocument(r.Context(), b.ID, b.Name, b.Tags, b.Body, b.Revision)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		code := 400
		if errors.Is(err, library.ErrMissing) {
			code = 404
		} else if errors.Is(err, library.ErrConflict) {
			code = 409
		}
		apiError(w, code, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *LibraryAPI) downloadDocument(w http.ResponseWriter, r *http.Request, id string) {
	d, err := a.store.Document(r.Context(), id)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	attachment(w, safeName(d.Item.Name)+".md")
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeContent(w, r, d.Item.Name, time.Time{}, strings.NewReader(d.Body))
}
