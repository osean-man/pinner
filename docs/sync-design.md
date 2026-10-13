# Pinner Sync: Design

Status: **implemented** · Author: Sean Callahan (with Claude) · 2026-10-12

This document describes how pinner's end-to-end encrypted sync works and why. The code is in
`internal/synccrypto` (keys and encryption), `internal/hlc` (clock), `internal/database` (merge rules),
`internal/syncer` (client) and `internal/server` + `cmd/pinner-server` (server).

## Goal

Sync pinned commands across one person's devices through a server that **can neither read nor modify** them.
Each user holds a secret key that never leaves their devices. The server is a dumb store of opaque blobs.

Out of scope: sharing pins between different people, key recovery, a web UI.

## Prior art: hiSHtory

hiSHtory ([design post](https://blog.daviddworken.com/posts/hishtory-explained/)) works like this:

- At install it generates a random `SecretKey`. To join another device, you copy that key over by hand (`hishtory init $SECRET`).
- It derives `UserId = HMAC(SecretKey, "user_id")` and `EncryptionKey = HMAC(SecretKey, "encryption_key")`, plus a random `DeviceId`.
- Entries are AES-GCM encrypted. The server is a **one-to-many queue**: every entry is enqueued once per device, and each device pulls its own queue.
- A new device bootstraps by posting a `DumpRequest`. An *existing* device must come online, notice it and upload its full history.
- The server sees ciphertext, entry counts, user and device IDs, and timing.

### Where pinner differs

| hiSHtory | pinner | Why |
|---|---|---|
| Shell history is append-only | Pins are **added, updated and removed** | Needs stable IDs, tombstones and a conflict rule |
| Per-device fan-out queues | **One encrypted record per pin**, pulled by cursor | Pins are few and mutable. Storing the latest state per record is simpler than queues. |
| Bootstrap needs another device online | A new device pulls everything from `since=0` | The server holds every record's latest ciphertext |
| Confidentiality is the main concern | **Integrity matters as much as secrecy** | Pinner *runs* what you select. A server able to forge or alter a pin would have remote code execution on every device. |

## Threat model

**Protected against a malicious or compromised server, or a network attacker:**

- Reading pin contents, labels or tags. Pins often contain credentials and hostnames.
- **Forging or altering a pin.** Every record is authenticated, and anything that fails is discarded with a loud
  warning, never stored and never run. The same applies to a blob moved to a different record slot or account,
  a truncated blob, and a blob encrypted with another account's key.
- **Rolling a pin back** on a device that has seen the newer version. A replayed older version loses the
  last-writer-wins comparison, and that device then re-uploads its newer version, so the server's copy is repaired.

**Not protected (accepted):**

- The server can **withhold** updates or deletes, or serve stale data to a device that has never seen the newer version.
- The server sees metadata: account ID, client IPs, number of records, blob sizes (padded to 256-byte buckets), and
  when changes happen.
- Anyone holding the secret can read and change all pins. **There is no recovery** if the secret and every device are lost.
- A compromised device is out of scope.

## Cryptography

Uses `golang.org/x/crypto` (`hkdf`, `chacha20poly1305`).

```
secret      = 32 random bytes, shown as PINR-XXXX-XXXX-…: base32 of secret || sha256(secret)[:2] (typo check)
user_id     = hex(HKDF-SHA256(secret, info="pinner/v1/user-id"))
auth_token  = hex(HKDF-SHA256(secret, info="pinner/v1/auth-token"))
enc_key     =     HKDF-SHA256(secret, info="pinner/v1/enc-key")
rid_key     =     HKDF-SHA256(secret, info="pinner/v1/record-id")

record_id   = hex(HMAC-SHA256(rid_key, pin.uuid))         # opaque and stable per pin
aad         = "pinner/v1|" + user_id + "|" + record_id
blob        = nonce(24 random bytes) || XChaCha20-Poly1305(enc_key, nonce, pad(plaintext), aad)
pad(p)      = uint32_be(len(p)) || p || zeros, rounded up to a multiple of 256 bytes
```

- **XChaCha20-Poly1305** takes a random 24-byte nonce, which is safe at any volume without tracking nonces.
- **The AAD binds each ciphertext to its account and record ID.** On top of that, the client checks that
  `record_id == HMAC(rid_key, plaintext.uuid)`, so a valid blob can't be presented as a different pin.
- The server stores `sha256(auth_token)`. Requests carry `X-Pinner-User: <user_id>` and
  `Authorization: Bearer <auth_token>`. Knowing the token doesn't reveal the secret or the encryption key.
- The client refuses plain `http://` unless the host is loopback (override: `PINNER_ALLOW_INSECURE=1`).

Plaintext record, before padding:

```json
{"v":1,"uuid":"…","command":"…","label":"…","tags":["…"],"sensitive":true,"created_at":1700000000,"hlc":"…","deleted":false}
```

Tombstones (`deleted: true`) carry only `uuid`, `hlc` and `created_at`; the command is stripped before encryption.
Usage counts and the local-only flag are **not** synced. They're per-device, and syncing usage would create a new
version every time a pin runs.

## Local data model

Schema version is tracked in `PRAGMA user_version`. Migrating from version 0 (the original schema):

1. Copies the database to `commands.db.bak` with `VACUUM INTO`.
2. Adds columns `uuid` (unique, backfilled), `label`, `tags`, `sensitive`, `local`, `use_count`, `last_used`,
   `hlc`, `deleted`, `dirty` and `synced`, plus a `meta` table (`device_id`, `hlc_last`, `pull_cursor`).

Local integer IDs remain the user-facing handle (`pinner rm 4`), and `uuid` is the sync identity. Removing a pin
leaves a tombstone. The database runs in WAL mode, and the file and directory are owner-only.

A test (`e2e/TestMigratesDatabaseFromOriginalRelease`) builds the original cgo-based release from git history,
creates pins with it, and checks that the new pure-Go build migrates them.

## Conflict resolution

**Last writer wins per pin**, ordered by a **hybrid logical clock** (`internal/hlc`). Timestamps are
`<16 hex wall-ms>.<8 hex counter>.<device id>`, fixed-width so string comparison gives the right order.
The clock is stored in `meta.hlc_last`, so it never goes backwards across runs, and it absorbs every remote timestamp
it sees. An edit made after seeing another device's edit therefore always wins, even if this device's clock is behind.
When two devices edit the same pin while out of contact, one edit wins and the other is lost. That's acceptable for
single commands.

Special cases in `Store.ApplyRemote`:

| Situation | Behaviour |
|---|---|
| Remote version newer | apply it; clear `dirty` |
| Same version (our own echo) | ignore |
| Remote version **older** than ours | ignore it and mark ours dirty, so it's re-uploaded. This repairs the server after a race or a replay. |
| Pin is local-only here | ignore remote |
| Unknown uuid, but an identical **never-synced** command exists here (typically when joining) | adopt the remote uuid and merge the two copies: newest non-empty label, union of tags, sensitive if either copy is. If the merge differs from the remote copy, give it a new version and push. |
| Unknown uuid otherwise | insert, including tombstones, so a later stale copy can't bring the pin back |

Tombstones are kept forever. They are tiny, and garbage-collecting them could let a long-offline device bring a
removed pin back.

## Server

`cmd/pinner-server`: Go `net/http` with SQLite through `modernc.org/sqlite`. Deployment is covered in [self-hosting.md](self-hosting.md).

```sql
users   (user_id TEXT PK, auth_hash BLOB, created_at INTEGER)
records (user_id, record_id, seq INTEGER, blob BLOB, updated_at, PRIMARY KEY (user_id, record_id))
-- every write sets seq = max(seq for the user) + 1, inside one transaction
```

| Endpoint | Purpose |
|---|---|
| `POST /v1/register` | Creates the account (201), or confirms the same token (200). A different token gives 403. Refused when `PINNER_MAX_USERS` is reached. |
| `POST /v1/records` `{"records":[{record_id, blob}]}` | Atomic batch upsert of up to 200 records. Returns `{"seqs":[…]}`. |
| `PUT /v1/records/{record_id}` (raw blob) | Single upsert |
| `GET /v1/records?since=<seq>` | `{"records":[{record_id, seq, blob}], "more": bool, "max_seq": n}`, at most 500 per page |
| `DELETE /v1/account` | Deletes the account and all its records (`pinner sync leave --purge`) |
| `GET /healthz` | Liveness |

Limits: 16 KiB per record, 10,000 records per account (`PINNER_MAX_RECORDS`), and an optional user cap. The server
doesn't rate-limit; do that at the proxy if needed.

## Client sync algorithm

```
sync():
  pull: GET since=cursor, page by page
          open + verify each blob (failures are reported and discarded, never applied)
          ApplyRemote(record)             # rules above
          save cursor after each page
  push: every dirty pin, in batches of 200
          local-only and never synced → just clear dirty
          local-only but synced before → upload a tombstone (removes it from other devices)
          otherwise                    → upload encrypted record
          on success: clear dirty only if the pin hasn't changed since it was read
```

**Server recovery.** If a client's cursor is beyond the server's `max_seq`, the server was restored from a backup or
lost data. The client resets (every pin is marked dirty and the cursor goes back to 0), pulls everything, and
re-uploads. New writes on the restored server get seq numbers below the old cursors, and without this reset clients
would skip them silently. If a request returns 403, the client tries to register: a 201 means the account was wiped,
so it re-uploads everything; a 403 means the token really is wrong, so the error is reported.

**Pull comes before push.** If push came first, a device holding an older edit would overwrite the server's
newer version, and a joining device would upload duplicates of pins the account already has. Both bugs showed up
in testing with push-first ordering.

When sync runs:
- Opening the menu syncs, waiting at most 1.5s.
- `add`, `update`, `remove` and `import` sync, waiting at most 3s.
- `pinner sync` syncs on demand.

Failures only print a one-line warning, and pinner keeps working offline. `PINNER_SYNC=off` disables automatic sync.

## CLI

```
pinner sync init --server URL    generate secret, register, upload existing pins, print secret (stdout)
pinner sync join SECRET --server URL
pinner sync                      sync now
pinner sync status | key | leave [--purge]
```

The server URL can also come from `PINNER_SERVER`. The secret and server URL are stored in
`~/.config/pinner/sync.json` (mode 0600), separately from the pins database.

## Decisions made

| Question | Decision |
|---|---|
| Hosting | Self-hosted. A Docker Compose setup with Caddy for TLS is in `deploy/`. Set `PINNER_MAX_USERS=1` for a personal server. |
| Server storage | SQLite only. Postgres or S3 can be added later behind the same API. |
| When to pull | When the menu opens, with a 1.5s limit. Changes push after every write. |
| Secret format | Base32 with a checksum, shown as `PINR-XXXX-…`. QR codes and word lists may come later. |
| Per-pin opt-out | `--local` |

## Tests

| Property | Test |
|---|---|
| Devices converge; edits, labels, tags and flags propagate | `syncer/TestTwoDevicesConverge`, `TestEditsAndMetadataPropagate`, `e2e/TestSyncBetweenDevicesThroughRealServer` |
| Concurrent edits resolve LWW regardless of sync order | `syncer/TestConcurrentEditLastWriterWins` |
| A stale overwrite on the server is repaired | `syncer/TestStaleOverwriteOnServerSelfHeals` |
| Deletes propagate and don't come back | `syncer/TestDeletePropagatesAndStaysDeleted` |
| Local-only pins never leave the device | `syncer/TestLocalPinsNeverLeaveTheDevice` |
| Joining doesn't duplicate; copies merge | `syncer/TestJoiningWithOverlappingPinsDoesNotDuplicate`, `database/TestAdoptionMergesIndependentCopies` |
| Server storage holds no plaintext | `syncer/TestServerNeverSeesPlaintext`, e2e raw-file check |
| Tampering, slot swapping, replays and forged records rejected | `syncer/TestTamperedBlobIsRejected`, `TestBlobMovedToAnotherSlotIsRejected`, `TestReplayedOldVersionIsIgnored`, `TestForgedRecordFromAnotherAccountIsRejected`, `synccrypto/TestOpenRejectsTampering` |
| Pagination (1,100 records), concurrent writes and batch atomicity | `syncer/TestPullPaginatesLargeAccounts`, `server/TestConcurrentPutsGetDistinctIncreasingSeqs`, `TestBatch*` |
| Restoring the server from a backup, or wiping it, recovers from the devices' copies | `syncer/TestServerRestoredFromOlderBackupConverges`, `TestServerWipedDevicesReupload`, `TestWrongTokenIsNotMistakenForAWipedServer` |
| An offline server doesn't block the CLI | `syncer/TestUnreachableServerFailsFast`, `e2e/TestCommandsKeepWorkingWhenServerIsDown` |

The tamper tests edit the server's SQLite database directly to act as a malicious server.

## Future work

- Rotating the secret (re-encrypt everything under a new key and retire the old account).
- Detecting a server that withholds data, e.g. by having devices publish signed counts of their records.
- Postgres or S3 storage backends; QR codes or word lists for entering the secret.
