package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (http.Handler, *Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.bin")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(st), st, path
}

func do(h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "naav.localhost:4747"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSeedAndRoundTrip(t *testing.T) {
	_, st, path := newTestServer(t)
	if got := len(st.List()); got != 2 {
		t.Fatalf("want 2 seeds, got %d", got)
	}
	if _, err := st.Create("Docs", "example.com/docs"); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := st2.Resolve("DOCS")
	if !ok || u != "https://example.com/docs" {
		t.Fatalf("got %q %v", u, ok)
	}
}

func TestRedirects(t *testing.T) {
	h, _, _ := newTestServer(t)
	cases := []struct{ path, want string }{
		{"/gh", "https://github.com"},
		{"/GH/org/repo", "https://github.com/org/repo"},
		{"/gh/org/repo?tab=issues", "https://github.com/org/repo?tab=issues"},
		{"/nope", "http://naav.localhost:4747/?create=nope"},
		{"/%25s", "/?setup=1"},
	}
	for _, c := range cases {
		rec := do(h, "GET", c.path, "", nil)
		if rec.Code != 302 || rec.Header().Get("Location") != c.want {
			t.Errorf("%s: code=%d loc=%q want %q", c.path, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
}

func TestCRUDAndGuards(t *testing.T) {
	h, _, _ := newTestServer(t)
	x := map[string]string{"X-Naav": "1"}

	if rec := do(h, "POST", "/api/aliases", `{"alias":"a","url":"b.com"}`, nil); rec.Code != 403 {
		t.Errorf("missing header should be 403, got %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/aliases", `{"alias":"a","url":"b.com"}`,
		map[string]string{"X-Naav": "1", "Origin": "https://evil.example"}); rec.Code != 403 {
		t.Errorf("foreign origin should be 403, got %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("foreign host should be 403, got %d", rec.Code)
	}

	if rec := do(h, "POST", "/api/aliases", `{"alias":"a","url":"b.com"}`, x); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/aliases", `{"alias":"A","url":"c.com"}`, x); rec.Code != 409 {
		t.Errorf("duplicate: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/aliases", `{"alias":"api","url":"c.com"}`, x); rec.Code != 400 {
		t.Errorf("reserved: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/aliases", `{"alias":"js","url":"javascript:alert(1)"}`, x); rec.Code != 400 {
		t.Errorf("bad scheme: %d", rec.Code)
	}
	if rec := do(h, "PUT", "/api/aliases/a", `{"alias":"z","url":"https://z.com"}`, x); rec.Code != 200 {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/z", "", nil); rec.Header().Get("Location") != "https://z.com" {
		t.Errorf("renamed alias: %q", rec.Header().Get("Location"))
	}
	if rec := do(h, "DELETE", "/api/aliases/z", "", x); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/aliases/z", "", x); rec.Code != 404 {
		t.Errorf("delete missing: %d", rec.Code)
	}
}

func TestExportImport(t *testing.T) {
	h, _, _ := newTestServer(t)
	x := map[string]string{"X-Naav": "1"}
	exp := do(h, "GET", "/api/export", "", nil)
	if exp.Code != 200 {
		t.Fatal(exp.Code)
	}
	do(h, "DELETE", "/api/aliases/gh", "", x)
	if rec := do(h, "POST", "/api/import?mode=merge", exp.Body.String(), x); rec.Code != 200 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/gh", "", nil); rec.Code != 302 || rec.Header().Get("Location") != "https://github.com" {
		t.Errorf("gh not restored")
	}
	if rec := do(h, "POST", "/api/import", "garbage", x); rec.Code != 400 {
		t.Errorf("garbage import: %d", rec.Code)
	}
}

func TestLaunchPlist(t *testing.T) {
	p := launchPlist("/Users/a&b/.naav/bin/naavd", 4747, "/tmp/x.log")
	for _, want := range []string{"dev.naav.naavd", "<string>4747</string>", "a&amp;b", "<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}
