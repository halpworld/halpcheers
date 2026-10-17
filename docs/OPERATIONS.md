# Deployment and Operations Guide

This document describes the production deployment, host preparation, zero-identifier proxy configuration, live maintenance, and observability runbook for Halp.

---

## 1. System Architecture & Data Residency (`eu-1`)

Halp is engineered to run as a single static Go binary (`halpd`) backed by a single local SQLite database file in Write-Ahead Logging (WAL) mode on an EU VPS (e.g. Hetzner CX22/CX32 in Falkenstein, Germany or Helsinki, Finland):

* **Strict EU Data Residency:** In accordance with `AGENTS.md` Data Rules and Decision 22, the service operates in a single canonical region: `eu-1`. No cross-border replication, no foreign database replicas, and no global lookup registries exist.
* **Zero Message Persistence:** In accordance with `AGENTS.md` Invariant 1, SQLite holds zero message rows, zero ping records, and zero communication logs. Only account keys (Argon2id hashes), public handles, delivery settings, group rosters, client-encrypted contact blobs, and durable abuse blocks are stored.
* **Capacity Budget:** A 2 vCPU / 4 GB RAM VPS supports sustained ingress of 1,200 RPS and 10,000–50,000 concurrent Server-Sent Events (SSE) connections at ~4.5–13 KB RAM per connection (totaling < 150 MB heap).

---

## 2. Host Preparation & Kernel Tuning

Each active desktop and web client holding a real-time appreciation stream maintains an open TCP socket. The host operating system must be tuned to handle tens of thousands of concurrent file descriptors and fast socket cycling.

### File Descriptor Ceilings

Set system-wide and user limits in `/etc/security/limits.d/99-halp.conf`:

```text
halp        soft    nofile    65536
halp        hard    nofile    65536
root        soft    nofile    65536
root        hard    nofile    65536
```

Verify active limit:

```bash
ulimit -n
# Output must be 65536 or higher
```

### Sysctl Network Tuning

Place the following kernel parameters in `/etc/sysctl.d/99-halp.conf` and apply with `sysctl --system`:

```ini
# Maximum open files
fs.file-max = 2097152

# Socket backlog for ingress bursts
net.core.somaxconn = 32768
net.ipv4.tcp_max_syn_backlog = 16384

# Ephemeral port range for outbound Web Push HTTP connections
net.ipv4.ip_local_port_range = 1024 65535

# Fast recycling of TIME_WAIT sockets
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15

# Memory management
vm.max_map_count = 262144
```

---

## 3. Production Deployment

### Method A: Docker Compose (Recommended)

The repository provides [`docker-compose.yml`](file:///Users/kenth/.gemini/antigravity/worktrees/halpcheers/continue_documentation_driven_development/docker-compose.yml), a production Compose definition coupling `halpd` with a hardened Caddy reverse proxy.

* **Loopback Binding (Invariant 9):** `halpd` binds strictly to host loopback (`127.0.0.1:8080:8080`). Direct inbound access from the public Internet is blocked, ensuring all external traffic passes through Caddy for TLS termination, header/IP scrubbing, and `/metrics` access gating.
* **Full-Stack Edge Container (`web/Dockerfile`):** Multi-stage build compiles TypeScript packages (`@halp/core` and `@halp/app`) and packages static web assets directly into `halp-caddy:latest`. Caddy serves the landing page (`/`), web app (`/app`, `/index.html`), Service Worker (`/sw.js` with `Service-Worker-Allowed: /`), and static assets (`/dist/*`, `/assets/*`), while proxying API routes (`/v1/*`, `/h/*`, `/@*`, `/badge/*`, `/overlay/*`, `/healthz`, `/readyz`) to `halpd:8080`.
* **Configuration Mount & SIGHUP Reload:** The container mounts `/etc/halp/halp.toml` and sets `HALP_CONFIG_FILE=/etc/halp/halp.toml`, enabling atomic runtime reloads via `docker kill -s HUP`.

#### 1. Directory Structure

```text
/opt/halp/
├── docker-compose.yml
├── Caddyfile
├── halp.toml
└── data/             # Persistent volume mounted to /data
```

#### 2. Configuration Setup

Copy the example configuration to `/opt/halp/halp.toml`:

```bash
cp halp.toml.example /opt/halp/halp.toml
```

Ensure `path = "/data/halp.db"` is set under `[db]`. In `docker-compose.yml`, mount `./halp.toml:/etc/halp/halp.toml:ro` (or use `./halp.toml.example:/etc/halp/halp.toml:ro`).

#### 3. Environment Variables

Create an environment file `/opt/halp/.env`:

```env
HALP_DOMAIN=halp.to
HALP_VAPID_SUBSCRIBER=mailto:ops@halp.to
```

#### 4. Launch Services

Build and launch the full-stack container environment:

```bash
docker compose up -d --build
```

#### 5. VAPID Key Retrieval

On initial boot, `halpd` generates an ECDSA P-256 VAPID keypair in `/data/vapid.key` if one is not present. Inspect the logs to copy the public key needed for Web Push subscribers:

```bash
docker compose logs halpd | grep -i vapid
```

---

### Method B: Bare-Metal systemd Deployment

For bare-metal Linux servers without Docker:

#### 1. Compile Static Binary

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/halpd ./server/cmd/halpd
```

#### 2. Create Service User & Directories

```bash
useradd -r -s /bin/false halp
mkdir -p /etc/halp /var/lib/halp
chown -R halp:halp /var/lib/halp
```

#### 3. Install Configuration

```bash
cp halp.toml.example /etc/halp/halp.toml
# Update db.path to "/var/lib/halp/halp.db"
```

#### 4. Systemd Unit File

Create `/etc/systemd/system/halpd.service`:

```ini
[Unit]
Description=Halp Appreciation Daemon
After=network.target

[Service]
Type=simple
User=halp
Group=halp
ExecStart=/usr/local/bin/halpd -config /etc/halp/halp.toml
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5s

# File descriptor ceiling for concurrent SSE streams
LimitNOFILE=65536

# Security sandboxing
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/halp

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
systemctl daemon-reload
systemctl enable --now halpd
```

---

## 4. Zero-Identifier Reverse Proxy Rules (Invariant 9)

**AGENTS.md Invariant 9** forbids logging identifiers (handles, account IDs, IP addresses, user agents, or subscription endpoints). The edge reverse proxy must uphold this invariant.

### Proxy Invariants:

1. **Scrub Client IP Addresses:** Access logs must not record `$remote_addr` or `X-Forwarded-For`.
2. **Scrub User Agents:** The `User-Agent` header is an identifying fingerprint across small user groups and must be stripped from proxy logs.
3. **No Path Parameter Logging:** Route patterns like `/v1/ping/{target}` or `/overlay/{token}` carry public handles or private tokens. Logs must record only the route template or strip query parameters.
4. **Block Public Metrics Access:** `/metrics` exposes Prometheus operational counters and must be blocked from the public Internet (`respond @metrics 403`). Internal scrapers query the backend directly over private networking.
5. **Disable Proxy Buffering:** For Server-Sent Events (`/v1/stream` and `/overlay/*`), the proxy must set `flush_interval -1` (or `proxy_buffering off` in Nginx) so appreciation events stream to viewers with sub-millisecond perceived latency.

### Caddy Implementation

See [`Caddyfile`](file:///Users/kenth/.gemini/antigravity/worktrees/halpcheers/continue_documentation_driven_development/Caddyfile) in the repository root for the production-ready Caddy configuration.

---

## 5. Live Configuration Reload (`SIGHUP`)

Halp divides tunables into two strict classes:

### Startup-Only Keys (Requires Process Restart)

In accordance with **AGENTS.md Invariant 8** (fixed-size allocations for anything keyed by user input), settings that allocate fixed memory structures at boot **cannot be resized at runtime**:

| Key | Default | Purpose |
| --- | --- | --- |
| `http.addr` | `:8080` | Network socket binding |
| `db.path` | `/data/halp.db` | SQLite database file path |
| `queue.size` | `65536` | Ingress ring buffer capacity |
| `dispatch.workers` | `4 * vCPU` | Dispatch worker pool size |
| `shutdown.drain_timeout` | `10s` | Deadline for draining the queue |
| `guard.expected_daily_pings` | `10000` | Sizing for pair Bloom filters and Count-Min sketch |
| `guard.pair.window` | `24h` | Rotation interval for Bloom filter windows |
| `guard.pair.max` | `3` | Daily sender-recipient limit for personal handles |
| `guard.pair.max_stream` | `10` | Daily sender-recipient limit for stream handles |

> [!WARNING]
> If a startup-only key is modified in `halp.toml` and a reload is triggered, `halpd` rejects the reload with a warning log and keeps the current running configuration intact. To change startup keys, restart the service.

### SIGHUP-Reloadable Keys (Hot-Swapped Atomically)

Operational thresholds and rate limits are swapped atomically via `atomic.Pointer` without connection drops or server restarts:

| Key | Default | Purpose |
| --- | --- | --- |
| `dispatch.max_age` | `30s` | Cutoff for stale un-dispatched ping jobs |
| `idle.demote_after` | `15m` | SSE inactivity duration before demoting to long-poll |
| `guard.sender.hour` | `20` | Max pings an account can send per hour |
| `guard.sender.day` | `100` | Max pings an account can send per day |
| `pow.floor_ms` | `10` | Target client CPU grind time (normal traffic) |
| `pow.signup_ms` | `1500` | Target client CPU grind time (signup) |
| `pow.epoch` | `5m` | PoW challenge rotation epoch duration |
| `digest.window_s` | `60` | Default notification coalescing window |
| `digest.max_per_hour` | `12` | Max digest notifications per hour |
| `groups.min_size` | `5` | Minimum active group size (deanonymisation floor) |
| `groups.max_members` | `500` | Max members per group |
| `groups.max_per_account` | `20` | Max groups per account |
| `alias.release_months` | `12` | Inactivity period before releasing aliases |
| `contacts.max_bytes` | `65536` | Max size of client-encrypted contacts blob |
| `blocks.ttl_days` | `365` | Lifespan for durable abuse mute blocks |
| `subscriptions.prune_after_days` | `180` | Idle lifespan for push subscriptions |

### Triggering a Reload

```bash
# Bare-metal
kill -HUP $(pidof halpd)
# or
systemctl reload halpd

# Docker Compose
docker kill -s HUP $(docker compose ps -q halpd)
```

Confirm reload in logs:
```text
config reloaded successfully on SIGHUP
```

---

## 6. SQLite WAL Online Backups & Maintenance

SQLite in WAL mode supports fully non-blocking online backups while `halpd` actively serves ingress traffic.

### Online Hot Backup

Because `halpd` runs on a minimal, non-root Distroless container image (`gcr.io/distroless/static-debian12:nonroot`) without a shell or package manager, hot backups under Docker Compose are performed using an ephemeral Alpine container mounting the `halpd-data` volume:

```bash
# Docker Compose (using an ephemeral container with sqlite3)
docker run --rm -v halpcheers_halpd-data:/data alpine:3.20 \
  sh -c "apk add --no-cache sqlite >/dev/null && sqlite3 /data/halp.db \".backup '/data/halp.db.bak'\""

# Bare-Metal
sqlite3 /var/lib/halp/halp.db ".backup '/var/lib/halp/backups/halp-$(date +%Y%m%d%H%M).db'"
```

### Verification & Integrity Check

Verify the snapshot before archiving offsite:

```bash
sqlite3 /var/lib/halp/backups/halp-latest.db "PRAGMA integrity_check;"
# Output: ok
```

### Daily Automated Maintenance

`halpd` automatically runs a background maintenance sweep every 6 hours:
1. **Block Expiration (`SweepExpiredBlocks`):** Prunes abuse mute rows (`(sender_account_id, handle, created_day)`) older than `blocks.ttl_days` (default 365 days).
2. **Dead Subscription Pruning:** Prunes inactive or expired Web Push endpoints (`subscriptions.prune_after_days`, default 180 days) as well as endpoints that returned `404 Not Found` or `410 Gone` (with timestamp check preventing re-registration races per Invariant 4).

---

## 7. Monitoring, Metrics & Alerting Runbook

Prometheus metrics are exposed on `/metrics`. In accordance with Invariant 9, metrics contain **zero personal identifiers** (no handles, no account IDs, no IP addresses).

### Key Prometheus Metrics

| Metric | Type | Description |
| --- | --- | --- |
| `halp_ingress_latency_seconds` | Histogram | Request latency for `POST /v1/ping/{target}` |
| `halp_pings_ingress_total` | Counter | Total inbound appreciation pings received |
| `halp_pings_dispatched_total` | Counter | Total pings delivered via push or SSE |
| `halp_pings_dropped_total` | Counter | Pings dropped (`reason="queue_full"`, `"rate_limit"`, `"blocked"`) |
| `halp_push_failures_total` | Counter | Push delivery errors by HTTP status code |
| `halp_sse_connections` | Gauge | Active SSE streams on `/v1/stream` and `/overlay/*` |
| `halp_pow_difficulty` | Gauge | Current adaptive Proof of Work difficulty floor (bits) |
| `halp_db_accounts_total` | Gauge | Total active accounts in `accounts` table |

### Production Alerts

#### 1. Ingress Latency Ceiling Violation (Invariant 2)

```yaml
alert: HalpIngressLatencyHigh
expr: histogram_quantile(0.99, rate(halp_ingress_latency_seconds_bucket[5m])) > 0.003
for: 2m
labels:
  severity: critical
annotations:
  summary: "p99 ingress latency exceeds 3 ms ceiling"
  description: "POST /v1/ping is blocking on downstream I/O or queue lock."
```

#### 2. Queue Saturation / Ping Drops

```yaml
alert: HalpQueueSaturated
expr: rate(halp_pings_dropped_total{reason="queue_full"}[5m]) > 0
for: 1m
labels:
  severity: warning
annotations:
  summary: "Ingress queue full, dropping pings"
  description: "Workers are failing to drain queue.size (65536). Check CPU saturation or Web Push dispatch concurrency."
```

#### 3. Web Push Delivery Degradation

```yaml
alert: HalpPushFailureRateHigh
expr: rate(halp_push_failures_total[5m]) / rate(halp_pings_dispatched_total[5m]) > 0.05
for: 5m
labels:
  severity: warning
annotations:
  summary: "Web Push failures exceed 5%"
  description: "Check outbound network connectivity to Apple (push.apple.com) and Google (fcm.googleapis.com)."
```

#### 4. Host File Descriptor Exhaustion

```yaml
alert: HalpFileDescriptorsNearLimit
expr: halp_sse_connections > 55000
for: 3m
labels:
  severity: critical
annotations:
  summary: "SSE connection count approaching 65536 FD limit"
  description: "High concurrency live stream or client surge. Prepare to increase host nofile limits or shard."
```
