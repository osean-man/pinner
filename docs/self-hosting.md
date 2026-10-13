# Self-hosting the sync server

`pinner-server` is a small Go binary that stores encrypted records in SQLite. It never sees your pins in
plaintext, but it does hold your account's access token hash and metadata, so run it on a machine you control
and put it behind HTTPS.

These steps target a DigitalOcean droplet (or any Linux VPS) using Docker Compose with Caddy, which gets and
renews a Let's Encrypt certificate automatically.

## 1. DNS and firewall

1. Create an `A` record (and `AAAA` for IPv6), e.g. `pins.example.com`, pointing at the droplet's IP.
2. Allow inbound 22, 80 and 443. Port 80 is needed for the certificate challenge and the HTTPS redirect.
   ```bash
   ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw enable
   ```
   The pinner container's port 8080 is only reachable by Caddy on the Docker network, not from the internet.

## 2. Run it

Releases publish a multi-arch image (amd64 and arm64) to `ghcr.io/osean-man/pinner-server`. The compose file uses
it, so the droplet doesn't need to build anything:

```bash
# on the droplet, with Docker and the compose plugin installed
git clone https://github.com/osean-man/pinner.git
cd pinner
cp deploy/.env.example deploy/.env
$EDITOR deploy/.env          # set PINNER_DOMAIN; PINNER_MAX_USERS=1 for a personal server
docker compose -f deploy/docker-compose.yml --env-file deploy/.env pull
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up -d
curl https://pins.example.com/healthz   # → ok
```

`PINNER_VERSION` in `.env` chooses the image tag. A major version (`1`) picks up compatible updates on the next `pull`,
and a full version (`1.0.0`) pins it exactly. To verify the image was built by this repository's release workflow:

```bash
gh attestation verify oci://ghcr.io/osean-man/pinner-server:1.0.0 --repo osean-man/pinner
```

To build from the checkout instead of pulling, add `--build` to `up`.

Then on your first device:

```bash
pinner sync init --server https://pins.example.com
```

Set `PINNER_MAX_USERS` to the number of accounts you expect (one account can cover all your devices). After the
limit is reached, the server refuses new registrations, so nobody else can store data on your server.

## 3. Backups and upgrades

- Data lives in the `pinner-data` Docker volume (`/data/pinner-server.db`). Back it up with:
  ```bash
  docker run --rm -v deploy_pinner-data:/data -v "$PWD":/backup alpine \
    sh -c 'cp /data/pinner-server.db* /backup/'
  ```
  The volume is named `<compose project>_pinner-data`; check it with `docker volume ls`. The backup holds only
  ciphertext.
- Losing or rolling back server data isn't fatal, because every device keeps a full local copy:
  - **Restored from an older backup:** each device notices the server is behind what it has already seen. On its
    next sync it uploads everything it has, so changes made after the backup come back.
  - **Data lost completely:** the next device to sync finds its account gone. It re-registers with the same secret
    and uploads its pins, and the other devices then fill in anything it was missing.

  Nothing needs to be done on the devices; just run `pinner sync` on each one, or wait for the next automatic sync.
- To upgrade, run `docker compose -f deploy/docker-compose.yml --env-file deploy/.env pull && docker compose -f deploy/docker-compose.yml --env-file deploy/.env up -d`.
  Also `git pull` if `deploy/` itself changed (compose file, Caddyfile).

## Without Docker

Release archives include a static `pinner-server` binary (`pinner-server --version` shows the build). Run it under systemd behind any TLS reverse proxy:

```ini
# /etc/systemd/system/pinner-server.service
[Unit]
Description=pinner sync server
After=network-online.target

[Service]
ExecStart=/usr/local/bin/pinner-server -addr 127.0.0.1:8080 -db /var/lib/pinner/pinner-server.db
Environment=PINNER_MAX_USERS=1
DynamicUser=yes
StateDirectory=pinner
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## Configuration

| Flag | Env | Default | |
|---|---|---|---|
| `-addr` | `PINNER_ADDR` | `127.0.0.1:8080` | listen address |
| `-db` | `PINNER_DB` | `pinner-server.db` | SQLite file |
| `-max-users` | `PINNER_MAX_USERS` | `0` (unlimited) | registration cap |
| `-max-records` | `PINNER_MAX_RECORDS` | `10000` | records per account |

Records are limited to 16 KiB each and uploads to batches of 200. The server doesn't rate-limit requests itself.
On a public server, add rate limiting at the proxy or firewall if you're worried about abuse.

## Local testing

```bash
go run ./cmd/pinner-server -db /tmp/pinner-test.db &
PINNER_CONFIG_DIR=/tmp/pinner-a pinner sync init --server http://localhost:8080
```

Plain `http://` is accepted for `localhost` and loopback addresses only.
