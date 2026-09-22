# Ciphertext relay

Mount `relay.NewHandler(store, relay.Config{})` at `/v1/relay` under the same TLS
server as control. `*control.Store` implements the required `Authorizer`. Close
the relay handler when the server shuts down.

Native clients connect using WebSocket with `Authorization: Bearer <device
token>`. Query tokens and requests carrying browser `Origin` are rejected.
All application frames are binary:

```
2 bytes: unsigned big-endian device-ID byte length (1..128)
N bytes: UTF-8 device ID
remaining bytes: opaque WireGuard packet (1..65535 bytes)
```

The device ID is the destination in client→server frames and the authenticated
source in server→client frames. The sender cannot forge another source. Both
devices must remain enrolled, unrevoked, and in the same mesh; self-forwarding
is rejected. Unauthorized destinations and malformed frames close the sender.
Packets to offline peers or full receiver queues are dropped, matching UDP
semantics; WireGuard/IP upper layers handle retransmission where applicable.

Defaults: 128 concurrent sessions, one per device, 64 queued packets per session,
10-second write deadline, 20-second ping and 60-second pong deadline. A revoked
device's session closes synchronously through the store notification and is
checked again before writes, preventing queued packets from being forwarded
after revocation. Established direct sessions must be revoked by mesh clients
refreshing the controller peer list independently.

`relay.Dial(ctx, url, token)` returns a `Client` implementing
`Send(peerID, []byte) error`, `Receive() (peerID, []byte, error)`, and `Close()`.
`DialWithConfig` accepts a custom trusted TLS configuration, for example a
private CA. Only loopback URLs may use unencrypted `ws`. Call `Receive`
continuously, including while idle, so WebSocket ping/pong processing works.
Connection reconnection and replay of unsent packets belong to the mesh layer;
the relay intentionally does not persist ciphertext or devices' private keys.
