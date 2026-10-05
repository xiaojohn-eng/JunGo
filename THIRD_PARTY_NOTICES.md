# Third-party software

JunGo-authored source is licensed under **GPL-3.0-only**; see the root `LICENSE`. Third-party code retains its own copyright and license, including the notices below. The root license does not relicense those dependencies.

JunGo embeds open-source components; original copyright and license notices remain applicable. This repository includes the corresponding integration source and reproducible build commands. Dependency versions are locked in go.mod/go.sum and Android Gradle scripts.

| Component | Upstream | License |
| --- | --- | --- |
| mihomo v1.19.31 | https://github.com/MetaCubeX/mihomo | GNU GPL v3 |
| wireguard-go | https://git.zx2c4.com/wireguard-go/ | MIT |
| gVisor | https://github.com/google/gvisor | Apache 2.0 |
| gorilla/websocket | https://github.com/gorilla/websocket | BSD 2-Clause |
| Go mobile | https://go.googlesource.com/mobile | BSD 3-Clause |
| AndroidX / Jetpack Compose | https://android.googlesource.com/platform/frameworks/support/ | Apache 2.0 |

`licenses/GPL-3.0.txt` contains the unchanged mihomo license. `licenses/go-modules/index.json` records the pinned Go dependency inventory and copied upstream notice filenames. Notices available at module roots, plus the nested `poly1305` and `chachapoly1305` notices from `github.com/metacubex/chacha v0.1.5`, are preserved under that directory; other notices remain in the corresponding upstream source. Binary redistribution must preserve relevant notices and provide the corresponding source required by those licenses. No device private keys, local registry, administrator tokens, or signing keys are part of the source distribution.

The matching mihomo v1.19.31 module source is included under `third_party/mihomo`
with its original licenses. Root `go.mod` selects this local copy to include a
small atomic log-level concurrency fix. Its provenance, exact patch scope, and
regression commands are documented in `third_party/mihomo/JUNGO-PATCHES.md`;
release source archives include this directory.

The Samsung product names and dimensional specifications are used to identify an adaptation target. The app is not affiliated with Samsung or Apple. Native interfaces are implemented in Kotlin/Compose and SwiftUI. Personal design drafts and device screenshots are not part of this public source snapshot.

## Release dependency scope

The GPLv2-only `github.com/rasky/go-lzo` module was removed from the 0.2.3
build graph before binary distribution. OpenVPN profiles requesting `comp-lzo:
yes` or `adaptive` are rejected during configuration, and an unexpected LZO
compressed data packet is rejected. OpenVPN connections without LZO remain
supported. This change is recorded in `third_party/mihomo/JUNGO-PATCHES.md`.
The published Go binaries must be checked with `go version -m` to confirm that
`github.com/rasky/go-lzo` is absent. The Go module notice inventory covers
the current build; a new dependency update requires a new license review.

The `github.com/metacubex/chacha` module has no root license file at this pinned
version. Its available nested MIT notices are copied verbatim and listed in the
inventory; original source headers remain with the upstream source.
