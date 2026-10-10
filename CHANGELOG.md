# Changelog

All notable changes to homeHash are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.3.0] - 2026-10-10

### Added

- Miner control (opt-in, `HOMEHASH_CONTROL=1`, off by default): pause/resume
  mining (`POST /api/miners/{id}/pause|resume`) and change the ASIC frequency
  / core voltage (`POST /api/miners/{id}/settings`); the firmware applies both
  live, no restart, and persists them
- Server-side clamps on accepted control values (100–750 MHz, 900–1300 mV by
  default — the firmware itself would accept 1–65535, which is no protection),
  overridable with `HOMEHASH_CONTROL_FREQ_MIN/MAX` and `HOMEHASH_CONTROL_VOLT_MIN/MAX`
- The dashboard reflects write access from `/api/stats`: miner cards gain
  Pause/Resume + Settings controls, a Paused chip tracks the device, and the
  settings dialog previews old → new values before applying
- Control endpoints reply 403 while control is off, 400 for malformed or
  out-of-clamp values, 404 for unknown miners, 501 when the driver has no
  control support, and 502 when the device rejects or fails the request
- Per-miner stats now carry the configured `frequency`, `core_voltage_mv` and
  device-reported `paused` state (history table migrates on open)
- `/api/health` reports the build-injected version instead of a stale constant

## [0.2.0] - 2026-10-09

### Added

- Downloadable release binaries for Linux, macOS and Windows (amd64 +
  arm64), packaged with a SHA-256 checksum file; the release workflow
  builds them automatically when a `v*` tag is pushed.
- `--version` flag, stamped with the release tag at build time by the
  release workflow
- Alerts surfaced in the dashboard: bell with an unread badge and a recent
  alerts panel; every fired alert is kept in the store and served via
  `GET /api/alerts`
- Alerts persisted with the miner's IP and model for display (database
  migrates on open)
- CSV and JSON history export: `GET /api/miners/{id}/export/{csv,json}`,
  served as a file attachment

## [0.1.0] - 2026-10-09

Initial release.

### Added

- Bitaxe driver (axeOS `/api/system/info`), tested against Gamma 601 and
  GammaTurbo on firmware v2.13.0 / v2.12.0
- Driver plugin architecture (`MinerDriver` + optional `InfoUpdater`)
- SQLite store: miner inventory, rolling stats history (WAL, 7-day window)
- Poller loop with per-miner driver dispatch
- Web dashboard: farm KPIs, per-miner cards, SVG charts (1h/6h/24h), table
  view, light/dark themes, single binary with embedded assets
- Live SSE stream (`/api/events`), REST API (`docs/API.md`)
- Network discovery: mDNS (`_axeos._sub._http._tcp` + `_http._tcp`) and IP
  range scan fallback, HTTP-verified, add-new-only store writes
- Alerting: offline / recovery / temperature thresholds with cooldown,
  log notifier plus Discord webhook
- systemd service and `make install`