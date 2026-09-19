package server

import (
	"bytes"
	"context"
	"encoding/json"
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
	request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航", "tags": []string{"科幻", "短篇"}}, 401)
	var auth struct{ CSRF string }
	json.Unmarshal(request("POST", "/api/auth/login", map[string]string{"password": "novel-test-password"}, 200), &auth)
	request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航"}, 403)
	csrf = auth.CSRF
	var book library.Item
	json.Unmarshal(request("POST", "/api/novels", map[string]any{"id": id, "name": "Night 夜航", "tags": []string{"科幻", "短篇"}}, 200), &book)
	var list struct{ Items []library.Item }
	json.Unmarshal(request("GET", "/api/library?module=novels&q=night&tag=科幻&tag=短篇", nil, 200), &list)
	if len(list.Items) != 1 {
		t.Fatal("combined search failed")
	}
	json.Unmarshal(request("GET", "/api/library?module=novels&tag=不存在", nil, 200), &list)
	if len(list.Items) != 0 {
		t.Fatal("tag AND filter ignored")
	}
	var chapter library.Chapter
	json.Unmarshal(request("POST", base+"/chapters", map[string]any{"id": library.NewID(), "title": "第一章", "body": "<script>不会执行</script>\n正文", "revision": book.Revision}, 200), &chapter)
	cp := base + "/chapters/" + chapter.ID
	request("PUT", cp, map[string]any{"title": "第一章", "body": "保存后的正文", "revision": chapter.Revision}, 200)
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
	request("GET", cp+"/versions", nil, 200)
	request("POST", cp+"/versions/1", map[string]int{"revision": 4}, 200)
	request("DELETE", "/api/items/"+id, nil, 200)
	request("GET", cp, nil, 404)
	request("POST", "/api/items/"+id+"/restore", nil, 200)
	request("GET", cp, nil, 200)
	request("POST", "/api/auth/logout", nil, 200)
	request("GET", cp+"/versions", nil, 401)
	request("GET", "/api/items/"+id+"/download", nil, 401)
}
