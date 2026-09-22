# Device file service

This package implements an HTTP API for explicit shared directories on Linux and
macOS. The host must serve `Service.Handler()` over authenticated HTTPS. It does
not listen on a port, issue device identities, or terminate TLS itself.

```go
service, err := files.New(files.Config{
    StateDir: "/var/lib/meshlink/files",
    Shares: []files.Share{
        {ID: "documents", Name: "Documents", Path: "/srv/documents", ReadOnly: false},
    },
    Authorize: func(ctx context.Context, request *http.Request) (string, error) {
        // Verify the device's current authorization/revocation state.
        // Return its stable, nonempty device identity, not a display name.
        return authenticatedDeviceID(request)
    },
})
```

`New` returns `(*Service, error)`. Call `Close` after stopping the HTTP server and
draining its active requests. A state directory is locked to one live service.
`StateDir` and each `Share.Path` must be absolute, and shares must exist. State and
shares cannot contain each other; shares cannot overlap. Only local operator
configuration selects physical directories. Responses never disclose those paths.
Shares/configuration are immutable while the service runs.

The authorizer is mandatory. Returning an error or an empty identity denies
access. Upload records belong to their initiating identity: another device gets
404 for status, chunk, completion, and cancellation. All authorized devices may
read the configured shares. Apply any narrower per-share policy in the host's
authorizer. The callback receives the original request and runs again throughout
transfer streams (at most 64 KiB per read), so it must be concurrency safe and
check current revocations. A revoked in-flight download ends early; because HTTP
headers may already have been sent, clients must reject truncated bodies rather
than treating HTTP 200 alone as success.

## Endpoints

All JSON errors have the shape
`{"error":{"code":"...","message":"...","expectedOffset":123}}`.
`expectedOffset` appears on offset conflicts only. Unknown routes are 404.

| Method and path | Input | Response |
| --- | --- | --- |
| `GET /v1/files/shares` | — | `{shares:[{id,name,readOnly}], maxChunkBytes}` |
| `GET /v1/files/entries?share=ID&path=RELATIVE` | Empty path means root | `{entries:[{name,path,directory,size,modified,version?}]}` |
| `POST /v1/files/mkdir` | `{shareId,path}` | `{path}`; recursively creates directories; idempotent |
| `GET` / `HEAD /v1/files/download?share=ID&path=RELATIVE` | Optional `Range`, see below | Streamed file, `ETag`, `Accept-Ranges`, `Content-Length` |
| `GET /v1/files/checksum?share=ID&path=RELATIVE` | Optional `If-Match` (recommended) | `{sha256,size,version}` |
| `POST /v1/files/uploads` | `{shareId,path,size,sha256,overwriteConfirmed?}` | 201 upload snapshot |
| `GET /v1/files/uploads/ID` | — | Upload snapshot |
| `PATCH /v1/files/uploads/ID` | Raw bytes; `Upload-Offset: N` | Updated snapshot |
| `POST /v1/files/uploads/ID/complete` | No body | Verified, published snapshot |
| `DELETE /v1/files/uploads/ID` | — | Persistently cancelled snapshot |
| `DELETE /v1/files/uploads/by-request/REQUEST_ID` | — | Cancel a sender-owned upload whose creation response was lost |

An upload snapshot is
`{id,shareId,path,size,offset,state,sha256}`. `state` is `pending`, `publishing`,
`completed`, or `cancelled`. `offset` counts durably acknowledged bytes, and `path`
becomes the actual chosen filename on completion. A client must persist the upload
ID and query status after interruption; it must not create a new upload for every
chunk retry. Paused tasks stay pending on the server; the client must persist the
user's paused/cancelled state and refrain from automatically restarting them.

Creation accepts an optional `X-Jungo-Upload-ID` header containing a stable client
task ID. Exact retries return the same persisted upload, including after restart
or cancellation; reuse with different content metadata returns 409. Keys are
scoped to the authenticated sender. This header is ignored by older services,
so clients remain compatible, but idempotent creation and cancellation by request
ID require an upgraded receiving service.

Uploads require a known nonnegative size and a 64-digit SHA-256 hex digest of the
whole file. Parent folders must be created with `mkdir` first. A zero-size file
needs no chunks. Exact retries of already acknowledged bytes are accepted and
return the current offset; mismatched retries or future offsets return 409.
Oversized files/chunks return 413. The default per-request chunk limit is
`DefaultMaxChunkBytes` (8 MiB), also advertised by the shares endpoint.

Completed/cancelled tasks reject further chunks. Repeating completion of an
already completed task succeeds without creating another file. A SHA-256 mismatch
returns 422 and never publishes; cancel that task and start a new upload. Completed
files cannot be deleted through this API. Publishing tasks must be resumed to
completion and cannot be cancelled once publication may have occurred.

By default, existing files are kept: `report.txt`, `report (1).txt`, etc. An
explicit `overwriteConfirmed:true` opts into replacing a regular file. Symlinks
and directories cannot be explicitly overwritten. File publication is atomic;
incomplete bytes are never exposed under the destination filename. Filesystems
must support atomic rename. Linux and Android use `renameat2(RENAME_NOREPLACE)`
relative to a directory handle opened through `os.Root`; the source and destination
are leaf names in that same directory. This preserves atomic no-overwrite behavior
without hardlinks, which Android's app SELinux policy prohibits. Other supported
platforms use an atomic hard link and therefore also need hardlink support. There
is no unsafe check-then-rename fallback when exclusive publication is unavailable.
Long colliding names are shortened at a UTF-8 boundary so the suffix fits the
255-byte filename limit while preserving the extension whenever possible.

## Resume and source changes

Download responses and regular-file listing entries expose a strong version ETag
based on filesystem device/inode, length, and nanosecond modification time.
Every resumed `Range` request **must** include the original ETag as `If-Match`:
missing versions return 428 and mismatched versions return 412. Restart the
download after 412. `If-Range` alone does not meet this requirement. A source
modified during an active read causes the stream to end early. Clients must
validate the expected body length and final content as appropriate. This version
is not a cryptographic content digest; a local writer deliberately restoring file
metadata is outside this change-detection guarantee.

After all download ranges are written, fetch `checksum` once with the original
`If-Match` and compare its SHA-256 against the complete local file. Hashing streams
with bounded memory, checks authorization throughout, and verifies that source
identity/size/modification time remain unchanged. A changed source returns 412;
revocation returns 403. No successful checksum is returned for a changing source.

## Storage and containment

Every client path is a validated relative slash-separated path. `os.Root` keeps
filesystem operations inside the configured share even when symlinks are present.
Absolute paths, traversal, backslashes, NULs, and reserved staging components are
rejected. Request paths containing symlinks are also rejected; listings omit
symlinks, special files, and reserved `.meshlink-upload-` staging names.
Share trees must not contain local bind mounts or special
filesystem interfaces exposing data the operator did not intend to share; these
are not isolation boundaries enforced by `os.Root`.

Upload parts and atomic JSON snapshots live in the private state directory
(0700, files 0600). On restart, parts are truncated to their last acknowledged
offset. Finalization streams a copy into a private reserved staging filename in
the destination directory, checks SHA-256, syncs it, and atomically publishes it.
This supports state/shares on different volumes and needs free space for a full
second copy at finalization. Staging names are excluded from all API listings and
rejected as request path components. Publication state is saved before rename/link
and completion is recovered by verifying the published file's digest after a
crash. New publication snapshots also identify the staging inode, so an existing
identical file cannot be mistaken for this upload before publication. A trusted
local operator still controls the files and filesystem contents.

Limits are configurable with `MaxUploadBytes`, `MaxPendingBytes`, `MaxChunkBytes`,
and `MinFreeBytes`. Defaults are 1 TiB/file, 2 TiB of reserved pending upload sizes,
8 MiB/chunk, and a 64 MiB free-space reserve. All values use bytes. Disk-full and
quota failures return 507. Upload/download bodies and hash verification stream
with bounded buffers rather than loading whole files into memory. Directory
listings and small metadata records are held in memory.

## Verification

Run `go test -race ./internal/files`. Tests cover containment and symlink escapes,
read-only shares, nested directories, interrupted/resumed multi-chunk uploads,
duplicate retries, concurrent filename collisions, publication crash recovery,
checksums, durable cancellation, owner separation, revocation during streams,
ETag-protected ranges, source replacement, limits, free-space errors, and a real
HTTP server round-trip. They do not claim 5 GB Android-device acceptance or
background/lock-screen validation; those require the integrated clients/devices.
