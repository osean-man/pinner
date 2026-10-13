// Package server implements the pinner sync server: an authenticated store of opaque encrypted blobs.
// It never sees plaintext and cannot produce blobs that clients will accept.
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	DefaultMaxBlob    = 16 << 10
	DefaultMaxRecords = 10000
	DefaultPageSize   = 500
	MaxBatch          = 200
)

// Options configures the server's limits.
type Options struct {
	MaxUsers   int // 0 means unlimited
	MaxRecords int // per user
	MaxBlob    int // bytes
	PageSize   int
}

// Store persists users and records in SQLite.
type Store struct {
	db *sql.DB
}

// OpenStore opens or creates the server database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			user_id TEXT PRIMARY KEY,
			auth_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS records (
			user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
			record_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			blob BLOB NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, record_id)
		);
		CREATE INDEX IF NOT EXISTS records_seq ON records(user_id, seq);
	`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("error initialising server database: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying database. Tests use it to simulate a malicious server.
func (s *Store) DB() *sql.DB { return s.db }

var (
	errForbidden = errors.New("forbidden")
	errFull      = errors.New("limit reached")
)

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Store) register(userID, token string, maxUsers int) (created bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var existing []byte
	err = tx.QueryRow("SELECT auth_hash FROM users WHERE user_id = ?", userID).Scan(&existing)
	if err == nil {
		if subtle.ConstantTimeCompare(existing, hashToken(token)) != 1 {
			return false, errForbidden
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if maxUsers > 0 {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM users").Scan(&n); err != nil {
			return false, err
		}
		if n >= maxUsers {
			return false, errFull
		}
	}
	if _, err := tx.Exec("INSERT INTO users(user_id, auth_hash, created_at) VALUES (?, ?, ?)", userID, hashToken(token), time.Now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) authenticate(userID, token string) error {
	var existing []byte
	err := s.db.QueryRow("SELECT auth_hash FROM users WHERE user_id = ?", userID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		// Hash anyway so unknown users take as long as known ones.
		_ = subtle.ConstantTimeCompare(hashToken(token), hashToken(""))
		return errForbidden
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(existing, hashToken(token)) != 1 {
		return errForbidden
	}
	return nil
}

// RecordIn is one record in a batch upload.
type RecordIn struct {
	RecordID string `json:"record_id"`
	Blob     []byte `json:"blob"`
}

func (s *Store) put(userID string, records []RecordIn, maxRecords int) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var count, seq int64
	if err := tx.QueryRow("SELECT count(*), COALESCE(MAX(seq), 0) FROM records WHERE user_id = ?", userID).Scan(&count, &seq); err != nil {
		return nil, err
	}
	seqs := make([]int64, 0, len(records))
	now := time.Now().Unix()
	for _, r := range records {
		var exists int
		if err := tx.QueryRow("SELECT count(*) FROM records WHERE user_id = ? AND record_id = ?", userID, r.RecordID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			if maxRecords > 0 && count >= int64(maxRecords) {
				return nil, errFull
			}
			count++
		}
		seq++
		_, err = tx.Exec(`INSERT INTO records(user_id, record_id, seq, blob, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(user_id, record_id) DO UPDATE SET seq = excluded.seq, blob = excluded.blob, updated_at = excluded.updated_at`,
			userID, r.RecordID, seq, r.Blob, now)
		if err != nil {
			return nil, err
		}
		seqs = append(seqs, seq)
	}
	return seqs, tx.Commit()
}

// RecordOut is one record returned by a pull.
type RecordOut struct {
	RecordID string `json:"record_id"`
	Seq      int64  `json:"seq"`
	Blob     []byte `json:"blob"`
}

// Page is the response to a pull.
type Page struct {
	Records []RecordOut `json:"records"`
	More    bool        `json:"more"`
	// MaxSeq is the account's highest seq. A client whose cursor is beyond it knows the server
	// lost data (e.g. was restored from a backup) and must re-sync from scratch.
	MaxSeq int64 `json:"max_seq"`
}

func (s *Store) list(userID string, since int64, limit int) (Page, error) {
	page := Page{Records: []RecordOut{}}
	// Read max seq before opening the row cursor: the store uses a single connection.
	if err := s.db.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM records WHERE user_id = ?", userID).Scan(&page.MaxSeq); err != nil {
		return Page{}, err
	}
	rows, err := s.db.Query("SELECT record_id, seq, blob FROM records WHERE user_id = ? AND seq > ? ORDER BY seq LIMIT ?", userID, since, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r RecordOut
		if err := rows.Scan(&r.RecordID, &r.Seq, &r.Blob); err != nil {
			return Page{}, err
		}
		page.Records = append(page.Records, r)
	}
	if len(page.Records) > limit {
		page.Records, page.More = page.Records[:limit], true
	}
	return page, rows.Err()
}

func (s *Store) deleteUser(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM records WHERE user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM users WHERE user_id = ?", userID); err != nil {
		return err
	}
	return tx.Commit()
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Handler serves the sync API.
type Handler struct {
	store *Store
	opts  Options
	mux   *http.ServeMux
}

// NewHandler returns the HTTP handler for the sync API.
func NewHandler(store *Store, opts Options) *Handler {
	if opts.MaxBlob <= 0 {
		opts.MaxBlob = DefaultMaxBlob
	}
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = DefaultMaxRecords
	}
	if opts.PageSize <= 0 {
		opts.PageSize = DefaultPageSize
	}
	h := &Handler{store: store, opts: opts, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	h.mux.HandleFunc("POST /v1/register", h.register)
	h.mux.HandleFunc("PUT /v1/records/{id}", h.authed(h.putRecord))
	h.mux.HandleFunc("POST /v1/records", h.authed(h.putBatch))
	h.mux.HandleFunc("GET /v1/records", h.authed(h.listRecords))
	h.mux.HandleFunc("DELETE /v1/account", h.authed(h.deleteAccount))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func credentials(r *http.Request) (userID, token string, ok bool) {
	userID = r.Header.Get("X-Pinner-User")
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return userID, token, found && hex64.MatchString(userID) && hex64.MatchString(token)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	userID, token, ok := credentials(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or malformed credentials")
		return
	}
	created, err := h.store.register(userID, token, h.opts.MaxUsers)
	switch {
	case errors.Is(err, errForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, errFull):
		writeError(w, http.StatusServiceUnavailable, "this server is not accepting new users")
	case err != nil:
		h.internalError(w, err)
	case created:
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

type authedHandler func(w http.ResponseWriter, r *http.Request, userID string)

func (h *Handler) authed(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, token, ok := credentials(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed credentials")
			return
		}
		if err := h.store.authenticate(userID, token); errors.Is(err, errForbidden) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		} else if err != nil {
			h.internalError(w, err)
			return
		}
		next(w, r, userID)
	}
}

func (h *Handler) putRecord(w http.ResponseWriter, r *http.Request, userID string) {
	recordID := r.PathValue("id")
	if !hex64.MatchString(recordID) {
		writeError(w, http.StatusBadRequest, "invalid record id")
		return
	}
	blob, err := readLimited(w, r, h.opts.MaxBlob)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "record too large")
		return
	}
	if len(blob) == 0 {
		writeError(w, http.StatusBadRequest, "empty record")
		return
	}
	seqs, err := h.store.put(userID, []RecordIn{{RecordID: recordID, Blob: blob}}, h.opts.MaxRecords)
	if errors.Is(err, errFull) {
		writeError(w, http.StatusInsufficientStorage, "record limit reached")
		return
	} else if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, map[string]int64{"seq": seqs[0]})
}

func (h *Handler) putBatch(w http.ResponseWriter, r *http.Request, userID string) {
	// Blobs are base64 in JSON, so allow for the 4/3 expansion plus framing.
	body, err := readLimited(w, r, MaxBatch*(h.opts.MaxBlob*4/3+128))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}
	var req struct {
		Records []RecordIn `json:"records"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed batch")
		return
	}
	if len(req.Records) == 0 || len(req.Records) > MaxBatch {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a batch must hold 1 to %d records", MaxBatch))
		return
	}
	for _, rec := range req.Records {
		if !hex64.MatchString(rec.RecordID) {
			writeError(w, http.StatusBadRequest, "invalid record id")
			return
		}
		if len(rec.Blob) == 0 || len(rec.Blob) > h.opts.MaxBlob {
			writeError(w, http.StatusRequestEntityTooLarge, "record empty or too large")
			return
		}
	}
	seqs, err := h.store.put(userID, req.Records, h.opts.MaxRecords)
	if errors.Is(err, errFull) {
		writeError(w, http.StatusInsufficientStorage, "record limit reached")
		return
	} else if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, map[string][]int64{"seqs": seqs})
}

func readLimited(w http.ResponseWriter, r *http.Request, max int) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, int64(max)))
}

func (h *Handler) listRecords(w http.ResponseWriter, r *http.Request, userID string) {
	since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil && r.URL.Query().Get("since") != "" {
		writeError(w, http.StatusBadRequest, "invalid since")
		return
	}
	limit := h.opts.PageSize
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l < limit {
		limit = l
	}
	page, err := h.store.list(userID, since, limit)
	if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, page)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request, userID string) {
	if err := h.store.deleteUser(userID); err != nil {
		h.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
