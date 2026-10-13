// Package syncer pushes and pulls end-to-end encrypted pins to a pinner sync server.
package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/osean-man/pinner/internal/database"
	"github.com/osean-man/pinner/internal/server"
	"github.com/osean-man/pinner/internal/synccrypto"
)

// EnvAllowInsecure permits plain-http servers that aren't on localhost.
const EnvAllowInsecure = "PINNER_ALLOW_INSECURE"

const configFile = "sync.json"

// Config is the device's sync configuration. It holds the secret, so it is stored with mode 0600,
// separately from the pins database.
type Config struct {
	Server string `json:"server"`
	Secret string `json:"secret"`
}

// LoadConfig returns the sync config in dir, or nil if sync isn't set up.
func LoadConfig(dir string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", configFile, err)
	}
	return &c, nil
}

// SaveConfig writes the sync config to dir with owner-only permissions.
func SaveConfig(dir string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, configFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveConfig deletes the sync config from dir.
func RemoveConfig(dir string) error {
	err := os.Remove(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ValidateServerURL normalises a server URL and refuses plain http except on loopback,
// because the bearer token would otherwise travel in the clear.
func ValidateServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server url %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) && os.Getenv(EnvAllowInsecure) != "1" {
			return "", fmt.Errorf("refusing plain http for %s: use https (or set %s=1 if you understand the risk)", host, EnvAllowInsecure)
		}
	default:
		return "", fmt.Errorf("server url must start with https://")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// Client talks to a sync server on behalf of one account.
type Client struct {
	base string
	keys synccrypto.Keys
	http *http.Client
}

// NewClient builds a client from a config.
func NewClient(c Config) (*Client, error) {
	base, err := ValidateServerURL(c.Server)
	if err != nil {
		return nil, err
	}
	secret, err := synccrypto.DecodeSecret(c.Secret)
	if err != nil {
		return nil, err
	}
	keys, err := synccrypto.DeriveKeys(secret)
	if err != nil {
		return nil, err
	}
	return &Client{base: base, keys: keys, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Keys returns the account keys.
func (c *Client) Keys() synccrypto.Keys { return c.keys }

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Pinner-User", c.keys.UserID)
	req.Header.Set("Authorization", "Bearer "+c.keys.AuthToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return out, resp.StatusCode, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(out))}
	}
	return out, resp.StatusCode, nil
}

// StatusError is an unsuccessful HTTP response from the server.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("server returned %d: %s", e.Code, e.Body) }

func isForbidden(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusForbidden
}

// Register creates the account on the server, or confirms this device holds the same secret.
func (c *Client) Register(ctx context.Context) error {
	_, err := c.register(ctx)
	return err
}

// register reports whether the account was newly created.
func (c *Client) register(ctx context.Context) (bool, error) {
	_, code, err := c.do(ctx, http.MethodPost, "/v1/register", nil)
	return code == http.StatusCreated, err
}

// DeleteAccount removes every record of this account from the server.
func (c *Client) DeleteAccount(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/v1/account", nil)
	return err
}

func (c *Client) putBatch(ctx context.Context, records []server.RecordIn) error {
	body, err := json.Marshal(map[string][]server.RecordIn{"records": records})
	if err != nil {
		return err
	}
	_, _, err = c.do(ctx, http.MethodPost, "/v1/records", body)
	return err
}

func (c *Client) list(ctx context.Context, since int64) (server.Page, error) {
	out, _, err := c.do(ctx, http.MethodGet, "/v1/records?since="+strconv.FormatInt(since, 10), nil)
	if err != nil {
		return server.Page{}, err
	}
	var p server.Page
	if err := json.Unmarshal(out, &p); err != nil {
		return server.Page{}, fmt.Errorf("malformed response from server: %w", err)
	}
	return p, nil
}

// Result summarises a sync run.
type Result struct {
	Pushed   int
	Received int     // records pulled from the server
	Applied  int     // pulled records that changed local pins
	Rejected []error // pulled records that failed authentication or validation and were discarded
}

const cursorKey = "pull_cursor"

// Sync pulls remote changes and then pushes local ones. Pulling first means a device never
// overwrites a newer version on the server with an older local edit, and lets a joining device
// match its existing pins to the account's before uploading duplicates.
//
// If the server no longer knows the account (it was wiped), Sync re-registers and uploads everything
// this device has, so the account is rebuilt from the devices' copies.
func Sync(ctx context.Context, store *database.Store, c *Client) (Result, error) {
	res, err := syncOnce(ctx, store, c)
	if !isForbidden(err) {
		return res, err
	}
	created, rerr := c.register(ctx)
	if rerr != nil || !created {
		// The account exists with a different token: a real authentication failure.
		return res, err
	}
	if err := store.ResetSyncState(); err != nil {
		return res, err
	}
	return syncOnce(ctx, store, c)
}

func syncOnce(ctx context.Context, store *database.Store, c *Client) (Result, error) {
	var res Result
	if err := pull(ctx, store, c, &res); err != nil {
		return res, err
	}
	err := push(ctx, store, c, &res)
	return res, err
}

func push(ctx context.Context, store *database.Store, c *Client, res *Result) error {
	dirty, err := store.Dirty()
	if err != nil {
		return err
	}
	var batch []server.RecordIn
	var pushed []database.Pin
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := c.putBatch(ctx, batch); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
		if err := store.MarkPushedAll(pushed); err != nil {
			return err
		}
		res.Pushed += len(batch)
		batch, pushed = batch[:0], pushed[:0]
		return nil
	}
	for _, p := range dirty {
		rec := p.ToRecord()
		if p.Local {
			if !p.Synced {
				// Never left this device: nothing to tell the server.
				if err := store.ClearDirty(p.UUID, p.HLC); err != nil {
					return err
				}
				continue
			}
			// It was synced before being made local: remove it from other devices.
			rec.Deleted = true
		}
		if rec.Deleted {
			// Tombstones carry no content.
			rec.Command, rec.Label, rec.Tags, rec.Sensitive = "", "", nil, false
		}
		plain, err := json.Marshal(struct {
			V int `json:"v"`
			database.Record
		}{1, rec})
		if err != nil {
			return err
		}
		rid := c.keys.RecordID(p.UUID)
		blob, err := c.keys.Seal(rid, plain)
		if err != nil {
			return err
		}
		batch = append(batch, server.RecordIn{RecordID: rid, Blob: blob})
		pushed = append(pushed, p)
		if len(batch) == server.MaxBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func pull(ctx context.Context, store *database.Store, c *Client, res *Result) error {
	raw, err := store.Meta(cursorKey)
	if err != nil {
		return err
	}
	since, _ := strconv.ParseInt(raw, 10, 64)
	for first := true; ; first = false {
		page, err := c.list(ctx, since)
		if err != nil {
			return fmt.Errorf("pull failed: %w", err)
		}
		if first && since > page.MaxSeq {
			// The server is behind what we've already seen: it was restored from a backup or lost
			// data. Start over and re-upload everything, so changes it lost come back and new
			// changes (which get seq numbers below our old cursor) aren't skipped.
			if err := store.ResetSyncState(); err != nil {
				return err
			}
			since = 0
			if page, err = c.list(ctx, since); err != nil {
				return fmt.Errorf("pull failed: %w", err)
			}
		}
		for _, r := range page.Records {
			res.Received++
			if r.Seq > since {
				since = r.Seq
			}
			changed, err := applyBlob(store, c.keys, r)
			if err != nil {
				res.Rejected = append(res.Rejected, fmt.Errorf("record %.12s…: %w", r.RecordID, err))
				continue
			}
			if changed {
				res.Applied++
			}
		}
		if err := store.SetMeta(cursorKey, strconv.FormatInt(since, 10)); err != nil {
			return err
		}
		if !page.More || len(page.Records) == 0 {
			return nil
		}
	}
}

func applyBlob(store *database.Store, keys synccrypto.Keys, r server.RecordOut) (bool, error) {
	plain, err := keys.Open(r.RecordID, r.Blob)
	if err != nil {
		return false, err
	}
	var rec struct {
		V int `json:"v"`
		database.Record
	}
	if err := json.Unmarshal(plain, &rec); err != nil {
		return false, fmt.Errorf("malformed record: %w", err)
	}
	if rec.V != 1 {
		return false, fmt.Errorf("unsupported record version %d (upgrade pinner)", rec.V)
	}
	// The record ID is derived from the UUID; a mismatch means the record was put in the wrong slot.
	if keys.RecordID(rec.UUID) != r.RecordID {
		return false, errors.New("record id does not match its contents")
	}
	return store.ApplyRemote(rec.Record)
}
