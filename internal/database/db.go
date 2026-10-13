// Package database stores pins in a local SQLite database (pure Go, no cgo).
package database

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/osean-man/pinner/internal/hlc"
	_ "modernc.org/sqlite"
)

// SchemaVersion is the latest schema version, stored in PRAGMA user_version.
const SchemaVersion = 2

var (
	ErrNotFound  = errors.New("pin not found")
	ErrDuplicate = errors.New("a pin with the same command already exists")
	ErrEmpty     = errors.New("command is empty")
)

// Pin is a pinned command.
type Pin struct {
	ID        int64
	UUID      string
	Command   string
	Label     string
	Tags      []string
	Sensitive bool
	Local     bool // never synced to other devices
	CreatedAt time.Time
	UseCount  int
	LastUsed  time.Time // zero if never used
	HLC       string
	Deleted   bool
	Dirty     bool // changed since the last successful push
	Synced    bool // has been pushed to or pulled from the sync server at least once
}

// NewPin holds the user-editable fields of a pin being added.
type NewPin struct {
	Command   string
	Label     string
	Tags      []string
	Sensitive bool
	Local     bool
}

// PinUpdate holds the fields to change on a pin. Nil fields are left unchanged.
type PinUpdate struct {
	Command   *string
	Label     *string
	Tags      *[]string
	Sensitive *bool
	Local     *bool
}

// Record is the synced representation of a pin. Usage stats and the local flag are deliberately absent:
// they are per-device and syncing them would create a new version on every run.
type Record struct {
	UUID      string   `json:"uuid"`
	Command   string   `json:"command"`
	Label     string   `json:"label,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Sensitive bool     `json:"sensitive,omitempty"`
	CreatedAt int64    `json:"created_at"`
	HLC       string   `json:"hlc"`
	Deleted   bool     `json:"deleted,omitempty"`
}

// Store is a handle on the pins database.
type Store struct {
	db    *sql.DB
	clock *hlc.Clock
	now   func() time.Time
}

// Open opens (creating and migrating if needed) the database at path. Before migrating an existing
// database to a new schema, it writes a backup next to it as <path>.bak.
func Open(path string) (*Store, error) {
	if err := ensureFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}
	// One connection serialises writers and keeps PRAGMAs and transactions simple.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.initClock(); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".bak"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	return s, nil
}

func ensureFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("error creating database file: %w", err)
	}
	return f.Close()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// SetNow replaces the store's wall clock. It exists for tests.
func (s *Store) SetNow(now func() time.Time) {
	s.now = now
	s.clock.SetNow(now)
}

func (s *Store) migrate(path string) error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("error reading schema version: %w", err)
	}
	if version >= SchemaVersion {
		return nil
	}

	var hasPins int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='pins'").Scan(&hasPins); err != nil {
		return fmt.Errorf("error inspecting database: %w", err)
	}
	if hasPins > 0 {
		backup := path + ".bak"
		_ = os.Remove(backup)
		if _, err := s.db.Exec("VACUUM INTO ?", backup); err != nil {
			return fmt.Errorf("error backing up database before migration: %w", err)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	steps := []func(*sql.Tx) error{migrateV1, migrateV2}
	for v := version; v < SchemaVersion; v++ {
		if err := steps[v](tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV1 upgrades the original schema (version 0): sync identity, labels, tags, flags and usage.
func migrateV1(tx *sql.Tx) error {
	// Version 0 is the original schema. CREATE IF NOT EXISTS covers brand new databases.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`ALTER TABLE pins ADD COLUMN uuid TEXT`,
		`ALTER TABLE pins ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pins ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pins ADD COLUMN sensitive INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pins ADD COLUMN local INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pins ADD COLUMN use_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pins ADD COLUMN last_used INTEGER`,
		`ALTER TABLE pins ADD COLUMN hlc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pins ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pins ADD COLUMN dirty INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE pins ADD COLUMN synced INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("error migrating database (%s): %w", firstLine(stmt), err)
		}
	}

	// Backfill a sync identity for every existing pin.
	deviceID := uuid.NewString()
	if _, err := tx.Exec("INSERT OR IGNORE INTO meta(key, value) VALUES ('device_id', ?)", deviceID); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT id FROM pins WHERE uuid IS NULL")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	clock := hlc.NewClock(deviceID, hlc.Timestamp{})
	for _, id := range ids {
		if _, err := tx.Exec("UPDATE pins SET uuid = ?, hlc = ? WHERE id = ?", uuid.NewString(), clock.Now().String(), id); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		if _, err := tx.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('hlc_last', ?)", clock.Last().String()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS pins_uuid ON pins(uuid)"); err != nil {
		return err
	}
	return nil
}

// migrateV2 adds the local-only table of last-used placeholder values. It is never synced:
// values are often specific to the machine (local ports, hosts on its network).
func migrateV2(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS placeholder_values (
		pin_id INTEGER NOT NULL REFERENCES pins(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (pin_id, name)
	)`)
	return err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (s *Store) initClock() error {
	deviceID, err := s.Meta("device_id")
	if err != nil {
		return err
	}
	if deviceID == "" {
		deviceID = uuid.NewString()
		if err := s.SetMeta("device_id", deviceID); err != nil {
			return err
		}
	}
	var last hlc.Timestamp
	if raw, err := s.Meta("hlc_last"); err != nil {
		return err
	} else if raw != "" {
		if last, err = hlc.Parse(raw); err != nil {
			return err
		}
	}
	s.clock = hlc.NewClock(deviceID, last)
	return nil
}

// DeviceID returns this database's device identifier.
func (s *Store) DeviceID() (string, error) {
	return s.Meta("device_id")
}

// tick issues a new HLC timestamp and persists it so the clock stays monotonic across runs.
func (s *Store) tick(tx *sql.Tx) (string, error) {
	ts := s.clock.Now().String()
	_, err := tx.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('hlc_last', ?)", ts)
	return ts, err
}

// Meta reads a value from the meta table, returning "" if it is unset.
func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes a value to the meta table.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)", key, value)
	return err
}

const pinColumns = `id, uuid, command, label, tags, sensitive, local, CAST(strftime('%s', timestamp) AS INTEGER),
	use_count, last_used, hlc, deleted, dirty, synced`

func scanPin(row interface{ Scan(...any) error }) (Pin, error) {
	var (
		p         Pin
		tags      string
		created   sql.NullInt64
		lastUsed  sql.NullInt64
		sensitive int
		local     int
		deleted   int
		dirty     int
		synced    int
	)
	err := row.Scan(&p.ID, &p.UUID, &p.Command, &p.Label, &tags, &sensitive, &local, &created,
		&p.UseCount, &lastUsed, &p.HLC, &deleted, &dirty, &synced)
	if err != nil {
		return Pin{}, err
	}
	p.Tags = SplitTags(tags)
	p.Sensitive, p.Local, p.Deleted, p.Dirty, p.Synced = sensitive != 0, local != 0, deleted != 0, dirty != 0, synced != 0
	if created.Valid {
		p.CreatedAt = time.Unix(created.Int64, 0)
	}
	if lastUsed.Valid {
		p.LastUsed = time.Unix(lastUsed.Int64, 0)
	}
	return p, nil
}

func (s *Store) queryPins(where string, args ...any) ([]Pin, error) {
	rows, err := s.db.Query("SELECT "+pinColumns+" FROM pins WHERE "+where+" ORDER BY id", args...)
	if err != nil {
		return nil, fmt.Errorf("error querying pins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pins []Pin
	for rows.Next() {
		p, err := scanPin(rows)
		if err != nil {
			return nil, err
		}
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

// SplitTags parses a comma-separated tag list, lower-casing, trimming and de-duplicating it.
func SplitTags(s string) []string {
	return NormalizeTags(strings.Split(s, ","))
}

// NormalizeTags lower-cases, trims and de-duplicates tags, keeping their order.
func NormalizeTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func joinTags(tags []string) string {
	return strings.Join(NormalizeTags(tags), ",")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Add stores a new pin. Adding a command that was previously removed revives that pin.
func (s *Store) Add(n NewPin) (Pin, error) {
	n.Command = strings.TrimSpace(n.Command)
	if n.Command == "" {
		return Pin{}, ErrEmpty
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Pin{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	var deleted int
	err = tx.QueryRow("SELECT id, deleted FROM pins WHERE command = ? ORDER BY deleted, id LIMIT 1", n.Command).Scan(&id, &deleted)
	switch {
	case err == nil && deleted == 0:
		return Pin{}, ErrDuplicate
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return Pin{}, err
	}

	ts, err := s.tick(tx)
	if err != nil {
		return Pin{}, err
	}
	if id != 0 { // reviving a removed pin
		_, err = tx.Exec(`UPDATE pins SET label = ?, tags = ?, sensitive = ?, local = ?, hlc = ?, deleted = 0, dirty = 1,
			use_count = 0, last_used = NULL, timestamp = CURRENT_TIMESTAMP WHERE id = ?`,
			n.Label, joinTags(n.Tags), boolInt(n.Sensitive), boolInt(n.Local), ts, id)
	} else {
		var res sql.Result
		res, err = tx.Exec(`INSERT INTO pins(uuid, command, label, tags, sensitive, local, hlc, dirty) VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
			uuid.NewString(), n.Command, n.Label, joinTags(n.Tags), boolInt(n.Sensitive), boolInt(n.Local), ts)
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return Pin{}, fmt.Errorf("error saving pin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Pin{}, err
	}
	return s.Get(id)
}

// Get returns an active (not removed) pin by its local ID.
func (s *Store) Get(id int64) (Pin, error) {
	p, err := scanPin(s.db.QueryRow("SELECT "+pinColumns+" FROM pins WHERE id = ? AND deleted = 0", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Pin{}, ErrNotFound
	}
	return p, err
}

// GetByUUID returns a pin, including a removed one, by its sync identity.
func (s *Store) GetByUUID(id string) (Pin, error) {
	p, err := scanPin(s.db.QueryRow("SELECT "+pinColumns+" FROM pins WHERE uuid = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Pin{}, ErrNotFound
	}
	return p, err
}

// List returns active pins, most frequently and recently used first. If tag is set, only pins with that tag are returned.
func (s *Store) List(tag string) ([]Pin, error) {
	pins, err := s.queryPins("deleted = 0")
	if err != nil {
		return nil, err
	}
	if tag = strings.ToLower(strings.TrimSpace(tag)); tag != "" {
		filtered := pins[:0]
		for _, p := range pins {
			for _, t := range p.Tags {
				if t == tag {
					filtered = append(filtered, p)
					break
				}
			}
		}
		pins = filtered
	}
	SortByFrecency(pins, s.now())
	return pins, nil
}

// Frecency scores a pin by how often and how recently it was used. A use loses half its weight every week.
func Frecency(p Pin, now time.Time) float64 {
	if p.UseCount == 0 || p.LastUsed.IsZero() {
		return 0
	}
	age := now.Sub(p.LastUsed)
	if age < 0 {
		age = 0
	}
	return float64(p.UseCount) * math.Pow(0.5, age.Hours()/(24*7))
}

// SortByFrecency orders pins by frecency, then most recently used, then oldest first.
func SortByFrecency(pins []Pin, now time.Time) {
	sort.SliceStable(pins, func(i, j int) bool {
		fi, fj := Frecency(pins[i], now), Frecency(pins[j], now)
		if fi != fj {
			return fi > fj
		}
		if !pins[i].LastUsed.Equal(pins[j].LastUsed) {
			return pins[i].LastUsed.After(pins[j].LastUsed)
		}
		return pins[i].ID < pins[j].ID
	})
}

// Update changes the given fields of an active pin.
func (s *Store) Update(id int64, u PinUpdate) (Pin, error) {
	cur, err := s.Get(id)
	if err != nil {
		return Pin{}, err
	}
	if u.Command != nil {
		c := strings.TrimSpace(*u.Command)
		if c == "" {
			return Pin{}, ErrEmpty
		}
		if c != cur.Command {
			var other int64
			err := s.db.QueryRow("SELECT id FROM pins WHERE command = ? AND deleted = 0 AND id != ?", c, id).Scan(&other)
			if err == nil {
				return Pin{}, ErrDuplicate
			} else if !errors.Is(err, sql.ErrNoRows) {
				return Pin{}, err
			}
		}
		cur.Command = c
	}
	if u.Label != nil {
		cur.Label = strings.TrimSpace(*u.Label)
	}
	if u.Tags != nil {
		cur.Tags = NormalizeTags(*u.Tags)
	}
	if u.Sensitive != nil {
		cur.Sensitive = *u.Sensitive
	}
	if u.Local != nil {
		cur.Local = *u.Local
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Pin{}, err
	}
	defer func() { _ = tx.Rollback() }()
	ts, err := s.tick(tx)
	if err != nil {
		return Pin{}, err
	}
	_, err = tx.Exec(`UPDATE pins SET command = ?, label = ?, tags = ?, sensitive = ?, local = ?, hlc = ?, dirty = 1 WHERE id = ?`,
		cur.Command, cur.Label, joinTags(cur.Tags), boolInt(cur.Sensitive), boolInt(cur.Local), ts, id)
	if err != nil {
		return Pin{}, fmt.Errorf("error updating pin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Pin{}, err
	}
	return s.Get(id)
}

// Remove deletes a pin. It is kept as a tombstone so the removal can sync to other devices.
func (s *Store) Remove(id int64) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ts, err := s.tick(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE pins SET deleted = 1, hlc = ?, dirty = 1 WHERE id = ?", ts, id); err != nil {
		return fmt.Errorf("error removing pin: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM placeholder_values WHERE pin_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// LastValues returns the placeholder values last used when running the pin on this device.
func (s *Store) LastValues(pinID int64) (map[string]string, error) {
	rows, err := s.db.Query("SELECT name, value FROM placeholder_values WHERE pin_id = ?", pinID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, rows.Err()
}

// SaveLastValues remembers placeholder values for the next run of the pin. Like usage stats, they are
// local-only: saving them neither marks the pin dirty nor advances its clock.
func (s *Store) SaveLastValues(pinID int64, values map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for name, value := range values {
		if _, err := tx.Exec("INSERT OR REPLACE INTO placeholder_values(pin_id, name, value) VALUES (?, ?, ?)", pinID, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordUse counts a run of the pin. Usage is local-only, so it neither marks the pin dirty nor advances its clock.
func (s *Store) RecordUse(id int64) error {
	_, err := s.db.Exec("UPDATE pins SET use_count = use_count + 1, last_used = ? WHERE id = ?", s.now().Unix(), id)
	return err
}

// Dirty returns pins (including removed and local ones) changed since their last successful push.
func (s *Store) Dirty() ([]Pin, error) {
	return s.queryPins("dirty = 1")
}

// MarkPushed clears the dirty flag, unless the pin changed again after the pushed version (hlc) was read.
func (s *Store) MarkPushed(pinUUID, pushedHLC string) error {
	_, err := s.db.Exec("UPDATE pins SET dirty = 0, synced = 1 WHERE uuid = ? AND hlc = ?", pinUUID, pushedHLC)
	return err
}

// MarkPushedAll is MarkPushed for many pins in one transaction.
func (s *Store) MarkPushedAll(pins []Pin) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range pins {
		if _, err := tx.Exec("UPDATE pins SET dirty = 0, synced = 1 WHERE uuid = ? AND hlc = ?", p.UUID, p.HLC); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearDirty clears the dirty flag without marking the pin as synced, for changes that never leave the device.
func (s *Store) ClearDirty(pinUUID, hlcAtRead string) error {
	_, err := s.db.Exec("UPDATE pins SET dirty = 0 WHERE uuid = ? AND hlc = ?", pinUUID, hlcAtRead)
	return err
}

// ToRecord converts a pin to its synced form.
func (p Pin) ToRecord() Record {
	return Record{
		UUID:      p.UUID,
		Command:   p.Command,
		Label:     p.Label,
		Tags:      p.Tags,
		Sensitive: p.Sensitive,
		CreatedAt: p.CreatedAt.Unix(),
		HLC:       p.HLC,
		Deleted:   p.Deleted,
	}
}

// ApplyRemote merges a record pulled from another device using last-writer-wins on the HLC.
// It reports whether local state changed.
func (s *Store) ApplyRemote(r Record) (bool, error) {
	remoteTS, err := hlc.Parse(r.HLC)
	if err != nil {
		return false, err
	}
	if _, err := uuid.Parse(r.UUID); err != nil {
		return false, fmt.Errorf("invalid record uuid: %w", err)
	}
	r.Command = strings.TrimSpace(r.Command)
	if r.Command == "" && !r.Deleted {
		return false, ErrEmpty
	}
	s.clock.Observe(remoteTS)

	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta(key, value) VALUES ('hlc_last', ?)", s.clock.Last().String()); err != nil {
		return false, err
	}

	cur, err := scanPin(tx.QueryRow("SELECT "+pinColumns+" FROM pins WHERE uuid = ?", r.UUID))
	switch {
	case err == nil:
		if cur.Local || r.HLC == cur.HLC {
			return false, tx.Commit()
		}
		if r.HLC < cur.HLC {
			// The server holds an older version than ours (a race between devices, or a replay).
			// Re-push ours so every device converges on the newest version.
			if cur.Synced {
				if _, err := tx.Exec("UPDATE pins SET dirty = 1 WHERE id = ?", cur.ID); err != nil {
					return false, err
				}
			}
			return false, tx.Commit()
		}
		_, err = tx.Exec(`UPDATE pins SET command = ?, label = ?, tags = ?, sensitive = ?, deleted = ?, hlc = ?, dirty = 0, synced = 1
			WHERE id = ?`, r.Command, r.Label, joinTags(r.Tags), boolInt(r.Sensitive), boolInt(r.Deleted), r.HLC, cur.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}

	// Unknown UUID. If this device already has the same command and it has never been synced
	// (typically: both machines had pins before the second one joined), adopt the remote identity
	// instead of creating a duplicate.
	if !r.Deleted {
		twin, err := scanPin(tx.QueryRow("SELECT "+pinColumns+" FROM pins WHERE command = ? AND deleted = 0 AND synced = 0 AND local = 0 ORDER BY id LIMIT 1", r.Command))
		if err == nil {
			// The two copies were created independently, so merge them field by field rather than
			// letting one overwrite the other: keep any label, union the tags, and stay sensitive if
			// either copy is.
			newer, older := Pin{Label: r.Label, Tags: r.Tags, Sensitive: r.Sensitive}, twin
			if twin.HLC > r.HLC {
				newer, older = twin, Pin{Label: r.Label, Tags: r.Tags, Sensitive: r.Sensitive}
			}
			label := newer.Label
			if label == "" {
				label = older.Label
			}
			tags := NormalizeTags(append(append([]string{}, newer.Tags...), older.Tags...))
			sensitive := newer.Sensitive || older.Sensitive

			if label == r.Label && joinTags(tags) == joinTags(r.Tags) && sensitive == r.Sensitive {
				// The merge is exactly the remote version.
				_, err = tx.Exec(`UPDATE pins SET uuid = ?, label = ?, tags = ?, sensitive = ?, hlc = ?, dirty = 0, synced = 1 WHERE id = ?`,
					r.UUID, label, joinTags(tags), boolInt(sensitive), r.HLC, twin.ID)
			} else {
				// The merge differs from what the server has: give it a new version and push it.
				var ts string
				if ts, err = s.tick(tx); err == nil {
					_, err = tx.Exec(`UPDATE pins SET uuid = ?, label = ?, tags = ?, sensitive = ?, hlc = ?, dirty = 1, synced = 1 WHERE id = ?`,
						r.UUID, label, joinTags(tags), boolInt(sensitive), ts, twin.ID)
				}
			}
			if err != nil {
				return false, err
			}
			return true, tx.Commit()
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}

	created := r.CreatedAt
	if created <= 0 {
		created = s.now().Unix()
	}
	_, err = tx.Exec(`INSERT INTO pins(uuid, command, label, tags, sensitive, local, timestamp, hlc, deleted, dirty, synced)
		VALUES (?, ?, ?, ?, ?, 0, datetime(?, 'unixepoch'), ?, ?, 0, 1)`,
		r.UUID, r.Command, r.Label, joinTags(r.Tags), boolInt(r.Sensitive), created, r.HLC, boolInt(r.Deleted))
	if err != nil {
		return false, fmt.Errorf("error inserting remote pin: %w", err)
	}
	return !r.Deleted, tx.Commit()
}

// ResetSyncState marks every pin as never synced and dirty, and forgets the pull cursor.
// It is used when this device joins or leaves a sync account.
func (s *Store) ResetSyncState() error {
	if _, err := s.db.Exec("UPDATE pins SET synced = 0, dirty = 1"); err != nil {
		return err
	}
	_, err := s.db.Exec("DELETE FROM meta WHERE key = 'pull_cursor'")
	return err
}
