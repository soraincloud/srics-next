package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/crypto/bcrypt"
)

func TestNovelHTTPFlowAndAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("novel-test-password"), bcrypt.MinCost)
	if err = l.Setup(hash); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(encoded))
		req.Header.Set("X-SRICS-Request", "app")
		req.Header.Set("X-SRICS-CSRF", csrf)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, out)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("novels response cacheable")
		}
		return out
	}
	id := library.NewID()
	base := "/api/novels/" + id
	request("GET", base, nil, 401)
	request("PUT", base+"/status", map[string]any{"completed": true, "revision": 1}, 401)
	request("PUT", base+"/progress", map[string]any{"chapter": library.NewID(), "paragraph": 1, "fraction": .5}, 401)
	request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航", "tags": []string{"科幻", "短篇"}}, 401)
	var auth struct{ CSRF string }
	json.Unmarshal(request("POST", "/api/auth/login", map[string]string{"password": "novel-test-password"}, 200), &auth)
	request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航"}, 403)
	csrf = auth.CSRF
	var book library.Item
	json.Unmarshal(request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航", "tags": []string{"科幻", "短篇"}}, 200), &book)
	if book.Completed {
		t.Fatal("new novel unexpectedly completed")
	}
	request("PUT", base+"/status", map[string]any{"revision": book.Revision}, 400)
	request("PUT", base+"/status", map[string]any{"completed": true, "revision": 0}, 400)
	oldRevision := book.Revision
	json.Unmarshal(request("PUT", base+"/status", map[string]any{"completed": true, "revision": oldRevision}, 200), &book)
	if !book.Completed {
		t.Fatal("completion status not returned")
	}
	request("PUT", base+"/status", map[string]any{"completed": true, "revision": oldRevision}, 200)
	request("PUT", base+"/status", map[string]any{"completed": false, "revision": oldRevision}, 409)
	csrf = "wrong"
	request("PUT", base+"/status", map[string]any{"completed": false, "revision": book.Revision}, 403)
	request("PUT", base+"/progress", map[string]any{"chapter": library.NewID()}, 403)
	csrf = auth.CSRF
	var list struct{ Items []library.Item }
	json.Unmarshal(request("GET", "/api/library?module=novels&q=night&tag=科幻&tag=短篇", nil, 200), &list)
	if len(list.Items) != 1 || !list.Items[0].Completed {
		t.Fatal("combined search failed")
	}
	json.Unmarshal(request("GET", "/api/library?module=novels&status=completed&q=night&tag=科幻&tag=短篇", nil, 200), &list)
	if len(list.Items) != 1 || !list.Items[0].Completed {
		t.Fatal("completed/name/tags filter failed")
	}
	json.Unmarshal(request("GET", "/api/library?module=novels&status=unfinished&q=night&tag=科幻", nil, 200), &list)
	if len(list.Items) != 0 {
		t.Fatal("completed novel leaked into unfinished filter")
	}
	request("GET", "/api/library?module=novels&status=invalid", nil, 400)
	request("GET", "/api/library?module=images&status=completed", nil, 400)
	json.Unmarshal(request("GET", "/api/library?module=novels&tag=不存在", nil, 200), &list)
	if len(list.Items) != 0 {
		t.Fatal("tag AND filter ignored")
	}
	json.Unmarshal(request("PATCH", "/api/items/"+id, map[string]any{"name": book.Name, "tags": []string{"悬疑", " 短篇 ", "悬疑"}, "revision": book.Revision}, 200), &book)
	json.Unmarshal(request("GET", "/api/library?module=novels&q=夜航&tag=悬疑&tag=短篇", nil, 200), &list)
	if len(list.Items) != 1 || len(list.Items[0].Tags) != 2 {
		t.Fatal("edited novel tags not searchable or not deduplicated")
	}
	json.Unmarshal(request("GET", "/api/library?module=novels&tag=科幻", nil, 200), &list)
	if len(list.Items) != 0 {
		t.Fatal("removed tag still matches novel")
	}
	var chapter library.Chapter
	json.Unmarshal(request("POST", base+"/chapters", map[string]any{"id": library.NewID(), "title": "第一章", "body": "<script>不会执行</script>\n正文", "revision": book.Revision}, 200), &chapter)
	cp := base + "/chapters/" + chapter.ID
	request("PUT", cp, map[string]any{"title": "第一章", "body": "保存后的正文\n第二段", "revision": chapter.Revision}, 200)
	request("PUT", cp, map[string]any{"title": "第一章", "body": "过期编辑", "revision": chapter.Revision}, 409)
	request("GET", "/api/novels/"+library.NewID()+"/chapters/"+chapter.ID, nil, 404)
	request("PUT", cp, map[string]any{"title": "第一章", "body": strings.Repeat("a", library.MaxChapterBody+1), "revision": 2}, 400)
	text := request("GET", "/api/items/"+id+"/download", nil, 200)
	if !strings.Contains(string(text), "保存后的正文") || !strings.Contains(string(text), "Night 夜航") {
		t.Fatal("wrong export", string(text))
	}
	request("GET", "/api/download?id="+id, nil, 400)
	request("POST", "/api/uploads", map[string]any{"id": library.NewID(), "module": "novels", "name": "invalid", "files": []map[string]any{{"name": "a.txt", "size": 1}}}, 400)
	request("DELETE", cp, map[string]int{"revision": 2}, 200)
	request("PUT", cp, map[string]any{"title": "第一章", "body": "修改已删除章节", "revision": 3}, 404)
	var state library.Novel
	json.Unmarshal(request("GET", base, nil, 200), &state)
	if len(state.Trash) != 1 || len(state.Chapters) != 0 {
		t.Fatal("deleted chapter not recoverable")
	}
	request("POST", cp+"/restore", map[string]int{"revision": 3}, 200)
	request("PUT", base+"/progress", map[string]string{"chapter": chapter.ID}, 200)
	request("PUT", base+"/progress", map[string]any{"chapter": chapter.ID, "paragraph": 1, "fraction": .625, "revision": 4}, 200)
	json.Unmarshal(request("GET", base, nil, 200), &state)
	if !state.Item.Completed || state.Bookmark == nil || state.Bookmark.Paragraph != 1 || state.Bookmark.Fraction != .625 {
		t.Fatal("missing detailed reading position/status", state)
	}
	for _, invalid := range []map[string]any{
		{"chapter": chapter.ID, "paragraph": -1}, {"chapter": chapter.ID, "paragraph": 2},
		{"chapter": chapter.ID, "fraction": 1.1}, {"chapter": chapter.ID, "fraction": "invalid"},
	} {
		request("PUT", base+"/progress", invalid, 400)
	}
	request("PUT", base+"/progress", map[string]any{"chapter": chapter.ID, "revision": 3}, 409)
	request("PUT", "/api/novels/"+library.NewID()+"/progress", map[string]any{"chapter": chapter.ID}, 404)
	request("GET", cp+"/versions", nil, 200)
	request("POST", cp+"/versions/1", map[string]int{"revision": 4}, 200)
	request("DELETE", "/api/items/"+id, nil, 200)
	request("GET", cp, nil, 404)
	request("POST", "/api/items/"+id+"/restore", nil, 200)
	request("GET", cp, nil, 200)
	request("POST", "/api/auth/logout", nil, 200)
	request("GET", cp+"/versions", nil, 401)
	request("GET", "/api/items/"+id+"/download", nil, 401)
	request("PUT", base+"/status", map[string]any{"completed": false, "revision": 1}, 401)
}

func TestNovelStatusFilterBeforePagination(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 52; i++ {
		it, e := l.CreateNovel(library.NewID(), "BOOK 共同小说", []string{"科幻", "分类"})
		if e != nil {
			t.Fatal(e)
		}
		if i < 45 {
			if _, e = l.SetNovelCompleted(it.ID, true, it.Revision); e != nil {
				t.Fatal(e)
			}
		}
	}
	unrelated, err := l.CreateNovel(library.NewID(), "其他小说", []string{"其他"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.SetNovelCompleted(unrelated.ID, true, unrelated.Revision); err != nil {
		t.Fatal(err)
	}
	a := LibraryAPI{store: l}
	get := func(query string) map[string]any {
		t.Helper()
		out, e := a.list(httptest.NewRequest("GET", "/api/library?module=novels&"+query, nil))
		if e != nil {
			t.Fatal(e)
		}
		return out.(map[string]any)
	}
	criteria := "q=book&tag=科幻&tag=分类"
	for _, filter := range []string{"", "status=all&"} {
		all := get(filter + criteria)
		if all["total"].(int) != 52 {
			t.Fatal("default/all filter changed results", all["total"])
		}
	}
	unfinished := get("status=unfinished&" + criteria)
	if unfinished["total"].(int) != 7 || unfinished["next"].(string) != "" {
		t.Fatal("incorrect unfinished total/pagination")
	}
	for _, it := range unfinished["items"].([]library.Item) {
		if it.Completed {
			t.Fatal("completed novel in unfinished list")
		}
	}
	first := get("status=completed&" + criteria)
	page := first["items"].([]library.Item)
	if first["total"].(int) != 45 || len(page) != 40 || first["next"].(string) == "" {
		t.Fatal("status filtering happened after pagination")
	}
	seen := map[string]bool{}
	for _, it := range page {
		if !it.Completed {
			t.Fatal("unfinished novel in completed list")
		}
		seen[it.ID] = true
	}
	// Cursor pagination must still work when a viewed item is deleted and a new
	// completed novel is added outside the original pagination snapshot.
	if err = l.Trash(page[0].ID, false); err != nil {
		t.Fatal(err)
	}
	added, err := l.CreateNovel(library.NewID(), "BOOK 新增小说", []string{"科幻", "分类"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.SetNovelCompleted(added.ID, true, added.Revision); err != nil {
		t.Fatal(err)
	}
	second := get(fmt.Sprintf("status=completed&%s&after=%s&snapshot=%d", criteria, first["next"], first["snapshot"]))
	if second["total"].(int) != 44 || len(second["items"].([]library.Item)) != 5 || second["next"].(string) != "" {
		t.Fatal("filtered second page is incomplete")
	}
	for _, it := range second["items"].([]library.Item) {
		if seen[it.ID] || it.ID == added.ID || !it.Completed {
			t.Fatal("duplicate, new or wrong-status novel in second page")
		}
	}
	if get("status=completed&tag=不存在")["total"].(int) != 0 {
		t.Fatal("empty combined filter ignored")
	}
	if get("status=completed&trash=1")["total"].(int) != 1 {
		t.Fatal("trashed novels leaked or were lost")
	}
}
