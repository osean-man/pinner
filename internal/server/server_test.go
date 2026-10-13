package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

type account struct{ user, token string }

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newAccount(t *testing.T) account { return account{randHex(t), randHex(t)} }

func newServer(t *testing.T, opts Options) (*httptest.Server, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(store, opts))
	t.Cleanup(func() { srv.Close(); _ = store.Close() })
	return srv, store
}

func do(t *testing.T, srv *httptest.Server, a account, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if a.user != "" {
		req.Header.Set("X-Pinner-User", a.user)
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func pull(t *testing.T, srv *httptest.Server, a account, since int64, limit int) Page {
	t.Helper()
	code, body := do(t, srv, a, "GET", fmt.Sprintf("/v1/records?since=%d&limit=%d", since, limit), nil)
	if code != 200 {
		t.Fatalf("pull: %d %s", code, body)
	}
	var p Page
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRegisterIsIdempotentAndBindsToken(t *testing.T) {
	srv, _ := newServer(t, Options{})
	a := newAccount(t)
	if code, _ := do(t, srv, a, "POST", "/v1/register", nil); code != http.StatusCreated {
		t.Fatalf("first register: %d", code)
	}
	if code, _ := do(t, srv, a, "POST", "/v1/register", nil); code != http.StatusOK {
		t.Fatalf("second device register: %d", code)
	}
	hijack := account{a.user, randHex(t)}
	if code, _ := do(t, srv, hijack, "POST", "/v1/register", nil); code != http.StatusForbidden {
		t.Fatalf("registering a known user id with another token must fail, got %d", code)
	}
	if code, _ := do(t, srv, account{"nothex", a.token}, "POST", "/v1/register", nil); code != http.StatusUnauthorized {
		t.Fatalf("malformed user id: %d", code)
	}
}

func TestRecordsRequireTheRightToken(t *testing.T) {
	srv, _ := newServer(t, Options{})
	a, b := newAccount(t), newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	do(t, srv, b, "POST", "/v1/register", nil)
	rid := randHex(t)
	if code, _ := do(t, srv, a, "PUT", "/v1/records/"+rid, []byte("blob-a")); code != 200 {
		t.Fatalf("put: %d", code)
	}

	if code, _ := do(t, srv, account{}, "GET", "/v1/records", nil); code != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", code)
	}
	if code, _ := do(t, srv, account{a.user, b.token}, "GET", "/v1/records", nil); code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := do(t, srv, account{a.user, b.token}, "PUT", "/v1/records/"+rid, []byte("evil")); code != http.StatusForbidden {
		t.Fatalf("wrong token write: %d", code)
	}
	if p := pull(t, srv, b, 0, 100); len(p.Records) != 0 {
		t.Fatalf("user b sees user a's records: %+v", p)
	}
	if p := pull(t, srv, a, 0, 100); len(p.Records) != 1 || string(p.Records[0].Blob) != "blob-a" {
		t.Fatalf("user a pull: %+v", p)
	}
}

func TestPutValidatesInput(t *testing.T) {
	srv, _ := newServer(t, Options{MaxBlob: 64, MaxRecords: 2})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	if code, _ := do(t, srv, a, "PUT", "/v1/records/../../etc", []byte("x")); code == 200 {
		t.Fatal("path traversal style id accepted")
	}
	if code, _ := do(t, srv, a, "PUT", "/v1/records/"+randHex(t), nil); code != http.StatusBadRequest {
		t.Fatalf("empty blob: %d", code)
	}
	if code, _ := do(t, srv, a, "PUT", "/v1/records/"+randHex(t), bytes.Repeat([]byte("x"), 65)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized blob: %d", code)
	}
	r1, r2 := randHex(t), randHex(t)
	do(t, srv, a, "PUT", "/v1/records/"+r1, []byte("1"))
	do(t, srv, a, "PUT", "/v1/records/"+r2, []byte("2"))
	if code, _ := do(t, srv, a, "PUT", "/v1/records/"+randHex(t), []byte("3")); code != http.StatusInsufficientStorage {
		t.Fatalf("record limit: %d", code)
	}
	if code, _ := do(t, srv, a, "PUT", "/v1/records/"+r1, []byte("1b")); code != 200 {
		t.Fatalf("updating an existing record at the limit must work: %d", code)
	}
}

func TestPullCursorReturnsOnlyNewerVersions(t *testing.T) {
	srv, _ := newServer(t, Options{})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	r1, r2 := randHex(t), randHex(t)
	do(t, srv, a, "PUT", "/v1/records/"+r1, []byte("v1"))
	do(t, srv, a, "PUT", "/v1/records/"+r2, []byte("x1"))
	first := pull(t, srv, a, 0, 100)
	cursor := first.Records[len(first.Records)-1].Seq

	do(t, srv, a, "PUT", "/v1/records/"+r1, []byte("v2"))
	next := pull(t, srv, a, cursor, 100)
	if len(next.Records) != 1 || next.Records[0].RecordID != r1 || string(next.Records[0].Blob) != "v2" {
		t.Fatalf("expected only the updated record, got %+v", next)
	}
	if all := pull(t, srv, a, 0, 100); len(all.Records) != 2 {
		t.Fatalf("an update must replace, not append: %+v", all)
	}
}

func TestPagination(t *testing.T) {
	srv, _ := newServer(t, Options{PageSize: 50})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	for i := 0; i < 120; i++ {
		do(t, srv, a, "PUT", "/v1/records/"+randHex(t), []byte{byte(i)})
	}
	var since int64
	seen := 0
	pages := 0
	for {
		p := pull(t, srv, a, since, 1000) // client asks for more than the server allows
		pages++
		seen += len(p.Records)
		if len(p.Records) > 50 {
			t.Fatalf("page size cap ignored: %d", len(p.Records))
		}
		since = p.Records[len(p.Records)-1].Seq
		if !p.More {
			break
		}
	}
	if seen != 120 || pages != 3 {
		t.Fatalf("saw %d records in %d pages, want 120 in 3", seen, pages)
	}
}

func TestConcurrentPutsGetDistinctIncreasingSeqs(t *testing.T) {
	srv, _ := newServer(t, Options{})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	const n = 40
	seqs := make(chan int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := do(t, srv, a, "PUT", "/v1/records/"+randHex(t), []byte("x"))
			if code != 200 {
				t.Errorf("put: %d %s", code, body)
				return
			}
			var out map[string]int64
			_ = json.Unmarshal(body, &out)
			seqs <- out["seq"]
		}()
	}
	wg.Wait()
	close(seqs)
	seen := map[int64]bool{}
	for s := range seqs {
		if seen[s] {
			t.Fatalf("duplicate seq %d", s)
		}
		seen[s] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct seqs, want %d", len(seen), n)
	}
}

func TestMaxUsers(t *testing.T) {
	srv, _ := newServer(t, Options{MaxUsers: 1})
	a := newAccount(t)
	if code, _ := do(t, srv, a, "POST", "/v1/register", nil); code != http.StatusCreated {
		t.Fatalf("first user: %d", code)
	}
	if code, _ := do(t, srv, newAccount(t), "POST", "/v1/register", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("second user should be refused: %d", code)
	}
	if code, _ := do(t, srv, a, "POST", "/v1/register", nil); code != http.StatusOK {
		t.Fatalf("existing user re-registering on a full server: %d", code)
	}
}

func TestDeleteAccount(t *testing.T) {
	srv, _ := newServer(t, Options{})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	do(t, srv, a, "PUT", "/v1/records/"+randHex(t), []byte("x"))
	if code, _ := do(t, srv, a, "DELETE", "/v1/account", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, srv, a, "GET", "/v1/records", nil); code != http.StatusForbidden {
		t.Fatalf("deleted account still authenticates: %d", code)
	}
	do(t, srv, a, "POST", "/v1/register", nil)
	if p := pull(t, srv, a, 0, 100); len(p.Records) != 0 {
		t.Fatalf("records survived account deletion: %+v", p)
	}
}

func batchBody(t *testing.T, recs []RecordIn) []byte {
	t.Helper()
	b, err := json.Marshal(map[string][]RecordIn{"records": recs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchPut(t *testing.T) {
	srv, _ := newServer(t, Options{MaxBlob: 64})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)

	recs := []RecordIn{{randHex(t), []byte("one")}, {randHex(t), []byte("two")}, {randHex(t), []byte("three")}}
	code, body := do(t, srv, a, "POST", "/v1/records", batchBody(t, recs))
	if code != 200 {
		t.Fatalf("batch: %d %s", code, body)
	}
	var out map[string][]int64
	_ = json.Unmarshal(body, &out)
	if s := out["seqs"]; len(s) != 3 || !(s[0] < s[1] && s[1] < s[2]) {
		t.Fatalf("seqs %v", s)
	}
	if p := pull(t, srv, a, 0, 100); len(p.Records) != 3 {
		t.Fatalf("pulled %d", len(p.Records))
	}

	// One invalid record rejects the whole batch.
	bad := []RecordIn{{randHex(t), []byte("ok")}, {"nothex", []byte("x")}}
	if code, _ := do(t, srv, a, "POST", "/v1/records", batchBody(t, bad)); code != http.StatusBadRequest {
		t.Fatalf("bad id in batch: %d", code)
	}
	tooBig := []RecordIn{{randHex(t), bytes.Repeat([]byte("x"), 65)}}
	if code, _ := do(t, srv, a, "POST", "/v1/records", batchBody(t, tooBig)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized record in batch: %d", code)
	}
	if p := pull(t, srv, a, 0, 100); len(p.Records) != 3 {
		t.Fatalf("rejected batches must write nothing, have %d records", len(p.Records))
	}
	over := make([]RecordIn, MaxBatch+1)
	for i := range over {
		over[i] = RecordIn{randHex(t), []byte("x")}
	}
	if code, _ := do(t, srv, a, "POST", "/v1/records", batchBody(t, over)); code != http.StatusBadRequest {
		t.Fatalf("oversized batch: %d", code)
	}
	if code, _ := do(t, srv, account{a.user, randHex(t)}, "POST", "/v1/records", batchBody(t, recs)); code != http.StatusForbidden {
		t.Fatalf("wrong token batch: %d", code)
	}
}

func TestBatchRespectsRecordLimitAtomically(t *testing.T) {
	srv, _ := newServer(t, Options{MaxRecords: 2})
	a := newAccount(t)
	do(t, srv, a, "POST", "/v1/register", nil)
	recs := []RecordIn{{randHex(t), []byte("1")}, {randHex(t), []byte("2")}, {randHex(t), []byte("3")}}
	if code, _ := do(t, srv, a, "POST", "/v1/records", batchBody(t, recs)); code != http.StatusInsufficientStorage {
		t.Fatalf("over-limit batch: %d", code)
	}
	if p := pull(t, srv, a, 0, 100); len(p.Records) != 0 {
		t.Fatalf("partial batch was written: %d records", len(p.Records))
	}
}
