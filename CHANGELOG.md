# Changelog

All notable changes to homeHash are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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