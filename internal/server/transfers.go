package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
)

func (a *LibraryAPI) transfers(w http.ResponseWriter, r *http.Request, access *vault.Access, parts []string) {
	var result any
	var err error
	switch {
	case len(parts) == 0 && r.Method == "GET":
		result, err = a.store.Transfers(access)
	case len(parts) == 0 && r.Method == "POST":
		var t library.Transfer
		if !decode(w, r, &t) {
			return
		}
		result, err = a.store.CreateTransfer(r.Context(), t, access)
	case len(parts) == 1 && library.IDPattern.MatchString(parts[0]) && r.Method == "DELETE":
		err = a.store.CancelTransfer(r.Context(), parts[0], access)
		if err == nil {
			err = a.store.Collect()
		}
	case len(parts) == 2 && library.IDPattern.MatchString(parts[0]) && parts[1] == "finish" && r.Method == "POST":
		if access != nil {
			select {
			case a.private.uploads <- struct{}{}:
				defer func() { <-a.private.uploads }()
			default:
				apiError(w, 429, library.ErrConflict)
				return
			}
		}
		result, err = a.store.FinishTransfer(r.Context(), parts[0], access, a.converter)
	case len(parts) == 3 && library.IDPattern.MatchString(parts[0]) && parts[1] == "chunks" && r.Method == "PUT":
		var index int
		index, err = strconv.Atoi(parts[2])
		if err == nil {
			r.Body = http.MaxBytesReader(w, r.Body, library.ChunkSize+1)
			result, err = a.store.ReceiveChunk(r.Context(), parts[0], index, r.Body, access)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if access != nil && err == nil {
		_, _, _, err = access.Snapshot()
	}
	if err != nil {
		privateError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func transferParts(path, prefix string) []string {
	v := strings.TrimPrefix(path, prefix)
	v = strings.Trim(v, "/")
	if v == "" {
		return nil
	}
	return strings.Split(v, "/")
}
