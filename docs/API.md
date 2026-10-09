# homeHash API Documentation

## Overview

homeHash exposes a JSON API for its embedded web dashboard and for programmatic
access. All endpoints return JSON; there is no authentication — homeHash is
designed to run on your LAN only, so please don't expose it to the internet.

Base URL: `http://localhost:8080/api`

The server also serves the dashboard itself: `GET /` returns the single-page UI
(built into the binary), and static assets live under `/static/`.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/health` | Service health check |
| GET | `/api/miners` | All miners with latest stats |
| GET | `/api/miners/{id}` | One miner's details + 24h history |
| GET | `/api/miners/{id}/history` | Historical stats, `hours`-limited |
| GET | `/api/stats` | Aggregate farm statistics |
| GET | `/api/events` | Server-Sent Events live stream |

### GET /api/health

```json
{
  "status": "ok",
  "timestamp": "2026-10-09T18:49:13Z",
  "version": "0.1.0"
}
```

### GET /api/miners

Returns all known miners ordered by ID, each with its latest stored stats and a
`status` derived from `last_seen` (online = polled within the last 5 minutes).

```json
{
  "miners": [
    {
      "id": "bitaxe-192-168-8-12",
      "ip": "192.168.1.10",
      "model": "Gamma 601",
      "driver": "bitaxe",
      "pool_url": "stratum+tcp://192.168.1.45:2018",
      "firmware": "v2.13.0",
      "mac": "AA:BB:CC:DD:EE:FF",
      "status": "online",
      "last_seen": "2026-10-09T22:55:14Z",
      "stats": {
        "hash_rate_gh": 1272.17,
        "accepted": 103104,
        "rejected": 58,
        "temperature_c": 54.375,
        "fan_speed": 80,
        "power_w": 20.899,
        "uptime_seconds": 3800235,
        "timestamp": "2026-10-09T22:55:14Z"
      }
    }
  ]
}
```

### GET /api/miners/{id}

Returns one miner as `{"miner": {...}}` — the same miner shape as above plus a
`history` array (up to the last 24 hours of poll results, oldest first):

```json
{
  "miner": {
    "id": "bitaxe-192-168-8-12",
    "...": "same fields as list entry",
    "history": [
      {"hash_rate_gh": 1272.2, "accepted": 103104, "rejected": 58, "temperature_c": 54.3, "fan_speed": 80, "power_w": 20.9, "uptime_seconds": 3800235, "timestamp": "..."}
    ]
  }
}
```

404 with `{"error": "..."}` when the ID is unknown.

### GET /api/miners/{id}/history

Historical stats for one miner.

**Query Parameters:**
- `hours` — window length, clamped to 1–168 (default 24)

**Response:**
```json
{
  "miner_id": "bitaxe-192-168-8-12",
  "hours": 6,
  "data": [
    {"hash_rate_gh": 1342.6, "accepted": 103104, "rejected": 58, "temperature_c": 54.25, "fan_speed": 80, "power_w": 20.86, "uptime_seconds": 3800240, "timestamp": "2026-10-09T22:55:19Z"}
  ]
}
```

### GET /api/stats

Aggregate farm statistics over miners polled recently. The dashboard's KPI
tiles are computed from this object.

```json
{
  "total_miners": 2,
  "online": 2,
  "offline": 0,
  "total_hashrate_gh": 3789.02,
  "avg_temperature_c": 55.31,
  "total_accepted": 201195,
  "total_rejected": 96,
  "total_power_w": 73.76,
  "efficiency_gh_per_w": 51.37,
  "timestamp": "2026-10-09T22:55:17Z"
}
```

`efficiency_gh_per_w` is 0 when no power data is available yet.

### GET /api/events (Server-Sent Events)

Live stream of farm snapshots. Each `message` carries one JSON event framed as
`data: {...}\n\n`; the server sends one every two seconds, and skips a frame
when nothing in the payload has changed. The dashboard consumes this instead of
polling; you can `curl -N` it too.

**Event payload:** `{ "farm": <same shape as /api/stats>, "miners": [<same
shape as the /api/miners entries>] }`

```json
{
  "farm": {"total_miners": 2, "online": 2, "total_hashrate_gh": 3789.0, "...": "..."},
  "miners": [{"id": "bitaxe-192-168-8-12", "...": "..."}]
}
```

Headers: `Content-Type: text/event-stream`, `Cache-Control: no-store`.

## Data Types

### Miner

| Field | Type | Description |
|-------|------|-------------|
| id | string | Unique identifier (seeded: `bitaxe-<ip with . and : → ->`) |
| ip | string | Local IP (may include port) |
| model | string | Hardware model (enriched from the device itself) |
| driver | string | Driver name ("bitaxe", etc.) |
| pool_url | string | Currently configured pool |
| firmware | string | Firmware version |
| mac | string | MAC address |
| status | string | "online", "offline", "warning" |
| last_seen | ISO 8601 | Last successful poll (nano precision) |

### Stats

| Field | Type | Description |
|-------|------|-------------|
| hash_rate_gh | float | Current hash rate in GH/s |
| accepted | int | Lifetime accepted shares |
| rejected | int | Lifetime rejected shares |
| temperature_c | float | Board temperature in Celsius |
| fan_speed | int | Fan speed percentage (0-100) |
| power_w | float | Current power draw in watts (0 if unknown) |
| uptime_seconds | int | Current uptime in seconds |
| timestamp | ISO 8601 | When stats were collected |

## Error Responses

Errors are a plain object:

```json
{"error": "human-readable message"}
```

Common HTTP status codes:
- 200 — Success
- 404 — Unknown path or miner id
- 405 — Method not allowed
- 500 — Internal server error

## Seeding Miners

While discovery matures, point homeHash at devices explicitly:

```bash
HOMEHASH_MINER=192.168.1.10,192.168.1.11 ./homeHash
```

The Bitaxe driver reads identity (model, firmware, MAC, pool) straight from the
device's `/api/system/info` on the first successful poll, so a fresh entry
fills in within seconds.