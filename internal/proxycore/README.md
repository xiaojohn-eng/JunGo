# Android mihomo embedding

The process owns one `Core`. Android gives `Start` the detached file descriptor
from its single `VpnService`; `Start` consumes that descriptor on success **and
failure**. Use `TUNFD: -1` on desktop or in tests. Build the Android Go library
with `-tags=cmfa,with_gvisor`. There is no second Android VPN: private mesh is an
outbound adapter backed by the independent, unprivileged WireGuard userspace stack.

The Android builder and this core agree on:

- IPv4 `172.19.0.1/30`, IPv6 `fdfe:dcba:9876::1/126`, MTU 1280.
- DNS `172.19.0.2`; DNS packets on port 53 are handled inside the TUN.
- Routes are configured by `VpnService.Builder`; Go never modifies OS routes.
- All applications stay inside the TUN. Application bypass changes public routing
  using UID metadata, never `addDisallowedApplication`, which would bypass mesh too.
- Every outer socket uses the `ProtectSocket` callback. Mesh and relay/control
  sockets must also be protected by their own transport configurations.

## Routing and live changes

Mihomo always remains in `Rule` mode. Immutable rules, in order, select:

1. `100.96.0.0/16` and `*.jungo.internal` → `JUNGO-MESH`.
2. Selected bypass UIDs → direct public internet.
3. Disabled proxy / direct mode → direct; global mode → selected public node.
4. Imported public rules, then selected public node as the fallback.

The TUN/loopback inbound also pins private destinations before mihomo's general
rule matching. Private traffic fails closed when a peer/name is unknown, removed,
or mesh is disabled. No private failure retries through an ordinary proxy.

`SetPolicy` swaps an atomic policy snapshot. `SetMesh` swaps the node/device-name
snapshot and closes existing streams when their peer/node is removed. Neither
restarts the TUN. Existing mesh TCP connections survive `LoadProfile`, which
validates and replaces outbounds, providers and rules. DNS **upstream settings**
from a new profile apply on the next `Start`; private names update immediately
through `SetMesh`. Private DNS names are answered locally with a short TTL;
unknown names return NXDOMAIN without reaching any public DNS upstream.

Node selection controls global mode and unmatched public traffic. In rule mode,
selector groups containing the chosen node use it; other imported rule targets
and automatic groups retain their configuration. Untested delays are `-1`.

## Imported configuration boundary

Only outbound proxies, groups, providers, rules, subrules, hosts and DNS are
imported. Controller endpoints, external UI, listeners, TUN descriptors, route
automation, NTP system-clock updates and other runtime settings are discarded.
Reserved `JUNGO-MESH`/`JUNGO-PUBLIC` names cannot be supplied by a profile.
Provider HTTP downloads require HTTPS and use generated cache paths under the
private state directory. File providers must remain within that directory,
including after resolving existing symlink ancestors. The optional app-created
SOCKS/HTTP listener accepts numeric loopback addresses only.

`ValidateProfile` performs full mihomo parsing when no core is running. During a
running session it validates the sanitized YAML structure; `LoadProfile` performs
full proposed-profile parsing before changing active routes. This avoids a
standalone validation call replacing a live parser's global runtime settings.
Mihomo itself uses process-global runtime configuration; callers must not create
or reconfigure another embedded mihomo instance outside this package.

## Verification

Tests use real loopback application sockets, real mihomo inbounds/outbounds and
two real WireGuard/gVisor nodes. They exercise TCP/UDP private traffic, mode and
UID precedence, live profile replacement preserving an existing private TCP
stream, DNS non-disclosure, disabling mesh, profile sanitization, and descriptor
consumption on startup failure. Android TUN establishment, manufacturer background
behavior, and packet flow on a physical phone still require device acceptance.
