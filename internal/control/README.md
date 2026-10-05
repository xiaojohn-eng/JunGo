# Control service

`NewStore(path)` opens a single-process JSON registry; `""` is in-memory.
`NewHandler(store, Config{AdminToken: token})` provides the HTTP API below. The
admin token must have at least 32 bytes and is supplied externally, never stored
in the registry. Production callers must use TLS. Enrollment tokens, pairing
codes and device public keys must never be logged. Private keys stay on devices.

| Method and path | Authorization | Request / response |
| --- | --- | --- |
| GET `/v1/info` | none | `service_id`, `version` |
| POST `/v1/pairing-codes` | admin bearer | `PairingCode` (10 minute default TTL) |
| POST `/v1/enroll` | single-use code | `Enrollment` → `EnrollmentResult` |
| GET `/v1/device` | device bearer | current `Device` |
| POST `/v1/device/name` | device bearer | `{ "name": string }` → updated own `Device` |
| GET `/v1/peers` | device bearer | `{ "peers": [Device] }` (excludes self and revoked) |
| GET `/v1/events` | device bearer | SSE `ready`, `peers-changed`, `revoked`, `heartbeat` |
| GET `/v1/devices` | admin bearer | `{ "devices": [Device] }` (includes revoked) |
| POST `/v1/heartbeat` | device bearer | `Heartbeat` → updated `Device` |
| POST `/v1/devices/{id}/revoke` | admin bearer | 204 |

`Enrollment` requires `service_id`, `code`, `name`, `public_key` (base64 encoded
32-byte WireGuard public key). Pairing data distributed by a trusted channel
must contain the HTTPS origin and `service_id`; the client must verify TLS and
match that service identity before submitting enrollment. The service ID is a
stable identifier, not a replacement for certificate validation or pinning.

`Heartbeat` accepts at most 16 IP:port `endpoints` and an optional HTTPS
`file_url`. These are peer advertisements; the controller does not connect to
them. Enrollment and heartbeats also accept `file_tls_fingerprint`, an optional
64-character SHA-256 certificate hex digest, normalized to lowercase and
published to authenticated peers. This is public pinning metadata, never a
private key. File clients must pin it and use independent device authentication;
never forward controller bearer tokens to a peer's advertised URL.
A device is displayed offline after 45 seconds without a heartbeat. Clients
heartbeat every 10 seconds. An unchanged heartbeat updates `last_seen` in memory
without a disk write or a global peer-change broadcast; liveness is refreshed
again after a restart. Endpoint, file URL and certificate changes remain durable.
Topology changes and return from offline trigger immediate invalidations; clients
still refresh authorization and peer liveness every 5 seconds as a fallback. Device private addresses
are allocated once from `100.96.0.0/16`, starting at `.2`; retired addresses are
never reused. Each installation currently contains one `default` mesh.

Device rename accepts 1–128 UTF-8 bytes with no surrounding whitespace,
control or formatting characters; ordinary spaces inside the name are allowed.
It changes only the authenticated device's display name, leaving its stable
hostname, IP address, public key and token unchanged. A committed change sends
`peers-changed` so other devices can refresh their peer lists.

Enrollment and revocation commit to disk before success. Only SHA-256 hashes of
random pairing codes and bearer tokens are persisted. Pairing codes are random
120-bit, one-use, time-limited secrets. Registry files are atomically replaced
with mode 0600. Run only one process against a registry file; mount it on local
storage and back it up as sensitive authorization data.

`Store.Authenticate`, `CanCommunicate` and `SubscribeRevocations` are the relay
authorization interface. Revocation callbacks run after persistence outside the
store lock, allowing live relay sessions to close before the API returns. Direct
WireGuard peers still require clients to refresh peer lists and remove revoked
keys; a control HTTP response cannot itself remove keys on an offline peer.

The authenticated SSE endpoint sends invalidation hints with a `device_id`;
fetch `/v1/peers` on `ready` or `peers-changed`. Hints can be coalesced and are not
a durable event log. A client's own revocation sends `revoked` and closes its
stream. Clients must reconnect and resync after disconnection, with a periodic
peer refresh as fallback. Streams send a heartbeat every 15 seconds and are
bounded to four per device and 128 total.
