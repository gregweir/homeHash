# Contributing

Thanks for your interest in homeHash!

## Development

```bash
make check   # build + vet + gofmt (zero diff) + all tests
```

CI runs `make check` on every push and pull request. Please make sure it
passes locally before opening a PR.

## Style

- Go code follows `gofmt` output exactly; the repo keeps a zero-diff gate.
- New behavior comes with tests (this project is built test-first; keep it
  that way).
- Keep dependencies minimal — the backend is stdlib-first by design and the
  frontend is framework-free.

## Adding miner support

New hardware goes in under `internal/drivers/` implementing
`base.MinerDriver` (plus `base.InfoUpdater` if the device can report its own
identity). Register the driver in `internal/drivers/` and test against real
hardware where possible; the Bitaxe driver's `doc.go` records what real
devices confirmed.

## Reporting bugs

Open an issue with: homeHash version, miner model, firmware version, and the
relevant log lines (redact anything private first — homeHash is local-only,
but still).