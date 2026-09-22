# Userspace mesh transport

`mesh.New(Config)` creates one official WireGuard device backed by gVisor's
userspace IPv4 TCP/UDP stack. It needs neither root nor an OS TUN interface.
Private keys are supplied by the application in hex or standard base64;
`GenerateKey` returns hex and does not persist them. Every peer is an explicit
`/32` route (both bare IPv4 and IPv4 `/32` inputs are accepted). `DialContext` rejects unknown
addresses and names, and never uses a host-network fallback.

`Config.Control` / `ProtectSocket` runs before the outer UDP socket is bound.
Android must use this to protect the socket against recursive VPN capture.
Control-plane and relay TLS sockets must be protected separately by their
respective dialers.

## Direct and relay routing

- Peers have up to 32 advertised IPv4 UDP endpoints; no DNS lookup occurs while
  accepting endpoints. `LocalEndpoints` returns non-loopback interface candidates.
- `DiscoverSTUN(ctx, "numeric-ip:port")` runs an RFC 5389 Binding transaction on
  the **same** UDP socket used by WireGuard. Responses must match both server
  source and a random 96-bit transaction ID. Publish the result through the
  authenticated control plane. STUN provides a candidate, never proof of peer
  reachability. Symmetric NAT may still require relay.
- For a self-hosted discovery endpoint, pass a UDP listener (normally port 3478)
  to `ServeSTUN`. It handles only IPv4 Binding requests, echoes the transaction
  ID, returns XOR-MAPPED-ADDRESS, and rate limits responses. It provides no TURN
  allocation or application-data forwarding. Closing its listener stops it.
- Once per second, candidates exchange domain-separated HMAC-SHA256 path probes
  with random 128-bit nonces. The authentication key is derived from the peer's
  X25519 shared secret; probes contain no application data. Only an authenticated
  matching response from the probed endpoint establishes a round trip. Received
  WireGuard ciphertext is **not** treated as proof before WireGuard authenticates it.
- A round-trip proof expires after three seconds. Proven LAN candidates take
  precedence over proven public candidates; otherwise the relay is selected.
  TCP retransmission bridges a brief path-failure interval. `ForceRelay` disables
  UDP data and probing for reproducible fallback tests.
- Relay implementations authenticate sender IDs. They transport unchanged
  WireGuard ciphertext; they receive no WireGuard private keys. The existing
  `internal/relay.Client` satisfies the `mesh.Relay` interface directly.
- `PeerStatus.Path` reports selected transport, **not** end-to-end application
  availability or remote presence. A relay connection alone does not prove a
  remote device online. Combine status with control-plane presence and service
  errors in the UI.

`SetPeer` updates endpoints without replacing the WireGuard device. To rotate a
public key, first remove the old peer. `RemovePeer` removes the crypto peer, route,
and transport candidates. Existing streams can no longer deliver packets through
that peer. `Close` stops UDP/probe/relay readers and closes the WireGuard stack.

## Application integration and limitations

Use `ListenTCP(port)` for the HTTPS shared-file server. Use `DialContext` for
mihomo's private-mesh outbound adapter. Resolve a stable device name against the
authenticated device list before dialing its numeric mesh address. Network-wide
device DNS is a separate application integration responsibility.

This stack exposes services that the application explicitly listens on or
forwards. It does **not** create a host-network virtual interface, expose every
Mac/Linux service automatically, or implement subnet routing. Forward host TCP/
UDP services explicitly, or add a separately privileged OS-TUN deployment path.
IPv6 peers, PMTU discovery, and sophisticated NAT-type-specific hole punching are
outside this minimal transport. The relay client must reconnect itself; a terminal
`Receive` error makes this Node's relay unavailable until the Node is recreated.

Tests create two complete WireGuard/netstack nodes and verify actual TCP and UDP
payloads over direct UDP, forced relay, and direct/relay/direct transitions. They
also cover revocation, rejecting arbitrary destinations, same-socket STUN mapping,
and the Android socket-protection hook.
