# JunGo local dependency patch

This directory is the complete Go module source for MetaCubeX/mihomo
**v1.19.31**, copied from the Go module cache whose version and checksums remain
pinned in the root `go.mod` and `go.sum`. Root `go.mod` uses a local `replace` so
desktop, server, tests, and Android gomobile builds all use the same source.

- Upstream release: https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.31
- Original source: https://github.com/MetaCubeX/mihomo/tree/v1.19.31
- License: `LICENSE` (GPL-3.0); nested upstream notices are retained unchanged.
- Upstream latest stable was v1.19.31 when checked on 2026-09-22. The Alpha
  branch's `log/log.go` also still used an ordinary, unsynchronized level value.

Only these upstream implementation changes have been made:

1. `log/log.go`: replace the mutable global `LogLevel` variable with
   `sync/atomic.Int32`, initialize it to INFO, and use atomic load/store in
   `Level`, `SetLevel`, and the print filter. The public API and severity
   semantics are unchanged.
2. `log/concurrency_test.go`: regression for simultaneous log-level changes and
   reads from old proxy cleanup. The original race was reproduced between
   `executor.ApplyConfig -> log.SetLevel` and adapter finalizer logging during
   repeated VPN startup.

Validation from the repository root:

```sh
go test -race github.com/metacubex/mihomo/log
go test -race -tags=cmfa,with_gvisor ./internal/proxycore ./internal/engine
```

Do not edit the user-global Go module cache. On a future dependency upgrade,
compare this small patch with upstream and remove the local replacement only
after the concurrency regression passes. The release source package explicitly
includes `third_party`, which is required to reproduce builds with this replace.

The root `.gitattributes` marks this copied source as vendored and leaves its
original upstream whitespace intact. Whitespace checks continue to cover the
project's own changes; this does not disable Go tests or the race detector.
