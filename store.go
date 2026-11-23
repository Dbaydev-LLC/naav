package main

import (
	"bytes"
	"compress/zlib"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// On-disk format: magic header + zlib(gob([]Record)).
const (
	fileMagic    = "NAAV1\n"
	maxDecoded   = 32 << 20 // guards against decompression bombs
	flushDelay   = 2 * time.Second
	maxAliasLen  = 64
	maxURLLength = 8192
)

var (
	ErrNotFound = errors.New("alias not found")
	ErrExists   = errors.New("alias already exists")

	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
	reserved = map[string]bool{"api": true, "favicon.ico": true}
)

// ValidationError is returned for bad user input.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

type Record struct {
	Alias     string    `json:"alias"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
	HitCount  uint64    `json:"hit_count"`
}

type Store struct {
	path string

	mu    sync.RWMutex
	m     map[string]*Record
	dirty bool
	timer *time.Timer

	saveMu sync.Mutex // serialises disk writes
}

func normalizeAlias(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", ValidationError("alias is required")
	}
	if len(s) > maxAliasLen {
		return "", ValidationError("alias is too long")
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("/\\?#%", r) {
			return "", ValidationError("alias cannot contain spaces or any of / \\ ? # %")
		}
	}
	if reserved[s] {
		return "", ValidationError(`"` + s + `" is reserved`)
	}
	return s, nil
}

func normalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ValidationError("destination URL is required")
	}
	if len(s) > maxURLLength {
		return "", ValidationError("URL is too long")
	}
	if !schemeRe.MatchString(s) {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", ValidationError("that doesn't look like a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ValidationError("only http and https URLs are supported")
	}
	return s, nil
}

// sanitize drops invalid records and fills in missing timestamps.
func sanitize(in []Record) []Record {
	out := make([]Record, 0, len(in))
	now := time.Now()
	for _, r := range in {
		a, err := normalizeAlias(r.Alias)
		if err != nil {
			continue
		}
		u, err := normalizeURL(r.URL)
		if err != nil {
			continue
		}
		r.Alias, r.URL = a, u
		if r.CreatedAt.IsZero() {
			r.CreatedAt = now
		}
		out = append(out, r)
	}
	return out
}

func encode(recs []Record) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(fileMagic)
	zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	if err := gob.NewEncoder(zw).Encode(recs); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decode(r io.Reader) ([]Record, error) {
	head := make([]byte, len(fileMagic))
	if _, err := io.ReadFull(r, head); err != nil || string(head) != fileMagic {
		return nil, errors.New("not a Naav backup file")
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("corrupt data: %w", err)
	}
	defer zr.Close()
	var recs []Record
	if err := gob.NewDecoder(io.LimitReader(zr, maxDecoded)).Decode(&recs); err != nil {
		return nil, fmt.Errorf("corrupt data: %w", err)
	}
	return sanitize(recs), nil
}

// OpenStore loads (or initialises) the alias file at path.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: path, m: map[string]*Record{}}

	f, err := os.Open(path)
	switch {
	case err == nil:
		recs, derr := decode(f)
		f.Close()
		if derr != nil {
			// Never silently destroy user data: move the bad file aside.
			bak := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
			if rerr := os.Rename(path, bak); rerr != nil {
				return nil, fmt.Errorf("%s is unreadable (%v) and could not be moved aside: %w", path, derr, rerr)
			}
			log.Printf("naavd: %v; moved to %s and starting fresh", derr, bak)
			s.seed()
		} else {
			for _, r := range recs {
				r := r
				s.m[r.Alias] = &r
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		s.seed()
	default:
		return nil, err
	}
	if s.dirty {
		if err := s.flush(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) seed() {
	now := time.Now()
	for _, r := range []Record{
		{Alias: "google", URL: "https://www.google.com"},
		{Alias: "gh", URL: "https://github.com"},
	} {
		r.CreatedAt = now
		s.m[r.Alias] = &r
	}
	s.dirty = true
}

func (s *Store) snapshotLocked() []Record {
	out := make([]Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// flush writes the current state atomically (tmp file + rename).
func (s *Store) flush() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.Lock()
	recs := s.snapshotLocked()
	s.dirty = false
	s.mu.Unlock()

	err := func() error {
		data, err := encode(recs)
		if err != nil {
			return err
		}
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, s.path)
	}()
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
	return err
}

// Close flushes any pending hit-count updates.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	dirty := s.dirty
	s.mu.Unlock()
	if dirty {
		return s.flush()
	}
	return nil
}

func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// Resolve looks up an alias (case-insensitive) and records the hit.
// Hit stats are persisted lazily so redirects never wait on disk.
func (s *Store) Resolve(alias string) (string, bool) {
	key := strings.ToLower(alias)
	s.mu.Lock()
	r, ok := s.m[key]
	if !ok {
		s.mu.Unlock()
		return "", false
	}
	r.HitCount++
	r.LastUsed = time.Now()
	target := r.URL
	s.dirty = true
	if s.timer == nil {
		s.timer = time.AfterFunc(flushDelay, s.lazyFlush)
	}
	s.mu.Unlock()
	return target, true
}

func (s *Store) lazyFlush() {
	s.mu.Lock()
	s.timer = nil
	s.mu.Unlock()
	if err := s.flush(); err != nil {
		log.Printf("naavd: saving hit counts failed: %v", err)
	}
}

func (s *Store) Create(alias, target string) (Record, error) {
	a, err := normalizeAlias(alias)
	if err != nil {
		return Record{}, err
	}
	u, err := normalizeURL(target)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	if _, ok := s.m[a]; ok {
		s.mu.Unlock()
		return Record{}, ErrExists
	}
	r := &Record{Alias: a, URL: u, CreatedAt: time.Now()}
	s.m[a] = r
	out := *r
	s.mu.Unlock()
	return out, s.flush()
}

// Update changes the URL and/or renames an alias, keeping its stats.
func (s *Store) Update(old, alias, target string) (Record, error) {
	old = strings.ToLower(old)
	a, err := normalizeAlias(alias)
	if err != nil {
		return Record{}, err
	}
	u, err := normalizeURL(target)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	r, ok := s.m[old]
	if !ok {
		s.mu.Unlock()
		return Record{}, ErrNotFound
	}
	if a != old {
		if _, exists := s.m[a]; exists {
			s.mu.Unlock()
			return Record{}, ErrExists
		}
		delete(s.m, old)
		r.Alias = a
		s.m[a] = r
	}
	r.URL = u
	out := *r
	s.mu.Unlock()
	return out, s.flush()
}

func (s *Store) Delete(alias string) error {
	key := strings.ToLower(alias)
	s.mu.Lock()
	if _, ok := s.m[key]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.m, key)
	s.mu.Unlock()
	return s.flush()
}

func (s *Store) Export() ([]byte, error) {
	return encode(s.List())
}

// Import merges (existing aliases keep their stats, URL is overwritten)
// or replaces the whole set.
func (s *Store) Import(recs []Record, replace bool) (added, updated int, err error) {
	recs = sanitize(recs)
	if len(recs) == 0 {
		return 0, 0, ValidationError("backup contains no valid aliases")
	}
	s.mu.Lock()
	if replace {
		s.m = map[string]*Record{}
	}
	for _, r := range recs {
		r := r
		if ex, ok := s.m[r.Alias]; ok {
			ex.URL = r.URL
			updated++
		} else {
			s.m[r.Alias] = &r
			added++
		}
	}
	s.mu.Unlock()
	return added, updated, s.flush()
}
