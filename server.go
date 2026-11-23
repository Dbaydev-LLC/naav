package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

const (
	maxJSONBody   = 64 << 10
	maxImportBody = 8 << 20
)

type Server struct {
	store *Store
}

func NewServer(st *Store) http.Handler {
	s := &Server{store: st}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/aliases", s.list)
	mux.HandleFunc("POST /api/aliases", s.create)
	mux.HandleFunc("PUT /api/aliases/{name}", s.update)
	mux.HandleFunc("DELETE /api/aliases/{name}", s.remove)
	mux.HandleFunc("GET /api/export", s.export)
	mux.HandleFunc("POST /api/import", s.importBackup)
	mux.HandleFunc("GET /{path...}", s.redirect)
	return guard(mux)
}

func localHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	// *.localhost always resolves to loopback inside the browser (RFC 6761),
	// so e.g. naav.localhost needs no DNS or /etc/hosts setup.
	return h == "127.0.0.1" || h == "::1" || h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// guard defends the local API against DNS rebinding (Host check) and
// cross-site requests (Origin check + required custom header on writes).
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !localHost(u.Host) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Naav") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Write(indexHTML)
}

// redirect handles GET /<alias>[/extra/path][?query].
func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	rawName, rest, _ := strings.Cut(p, "/")
	name, err := url.PathUnescape(rawName)
	if err != nil || name == "" || strings.EqualFold(name, "api") {
		http.NotFound(w, r)
		return
	}
	// An un-substituted search-engine template ("%s") means "no alias typed":
	// show the dashboard with the address-bar setup instructions open.
	if name == "%s" {
		http.Redirect(w, r, "/?setup=1", http.StatusFound)
		return
	}
	target, ok := s.store.Resolve(name)
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		http.Redirect(w, r, "http://"+r.Host+"/?create="+url.QueryEscape(name), http.StatusFound)
		return
	}
	http.Redirect(w, r, buildTarget(target, rest, r.URL.RawQuery), http.StatusFound)
}

// buildTarget appends the extra path (already escaped) and query to target.
func buildTarget(target, restEscaped, rawQuery string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	if restEscaped != "" {
		if dec, err := url.PathUnescape(restEscaped); err == nil {
			baseEsc := strings.TrimRight(u.EscapedPath(), "/")
			u.Path = strings.TrimRight(u.Path, "/") + "/" + dec
			u.RawPath = baseEsc + "/" + restEscaped
		}
	}
	if rawQuery != "" {
		if u.RawQuery == "" {
			u.RawQuery = rawQuery
		} else {
			u.RawQuery += "&" + rawQuery
		}
	}
	return u.String()
}

// ---- JSON API ----

type aliasBody struct {
	Alias string `json:"alias"`
	URL   string `json:"url"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ve ValidationError
	code := http.StatusInternalServerError
	switch {
	case errors.As(err, &ve):
		code = http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrExists):
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readBody(w http.ResponseWriter, r *http.Request) (aliasBody, bool) {
	var b aliasBody
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, ValidationError("invalid request body"))
		return b, false
	}
	return b, true
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.List())
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	rec, err := s.store.Create(b.Alias, b.URL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(b.Alias) == "" {
		b.Alias = r.PathValue("name")
	}
	rec, err := s.store.Update(r.PathValue("name"), b.Alias, b.URL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) export(w http.ResponseWriter, _ *http.Request) {
	data, err := s.store.Export()
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="naav-backup-%s.naav"`, time.Now().Format("2006-01-02")))
	w.Write(data)
}

func (s *Server) importBackup(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBody))
	if err != nil {
		writeErr(w, ValidationError("backup file is too large"))
		return
	}
	recs, err := decode(strings.NewReader(string(data)))
	if err != nil {
		writeErr(w, ValidationError(err.Error()))
		return
	}
	replace := r.URL.Query().Get("mode") == "replace"
	added, updated, err := s.store.Import(recs, replace)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"added": added, "updated": updated, "total": len(s.store.List()),
	})
}
