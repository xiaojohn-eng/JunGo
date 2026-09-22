# Persistent device messages

`New(snapshotPath)` opens a private (0700 directory, 0600 snapshot) revisioned
message store. `Put`, `Get`, `List`, and `MarkSynced` are safe for concurrent use.
Every mutation appends a private JSON record and synchronously calls `fsync`
before publication in memory. A bounded journal avoids rewriting unrelated
history on each attachment progress update. At 1,024 records or 8 MiB the
store writes a complete atomic snapshot before replacing the redundant journal.
The 512 MiB total snapshot limit still applies to the combined snapshot + log.

Startup replays complete records idempotently and truncates only a partial final
record. Complete malformed records, missing v2 journals, missing base snapshots,
and nonprivate/nonregular state fail closed. Runtime journal replacement or
truncation is also rejected; further writes require reopening/recovery.

Storage upgrades are explicit: version 1 snapshots remain readable, while an
active journal uses snapshot version 2 so older binaries cannot silently load a
stale subset of the history. `Close()` seals all subsequent writes, checkpoints
the complete history in version 1 format and clears the redundant journal.
The engine calls it after both workers stop. A successful normal shutdown thus supports
rollback to the old binary. Check `Close()`/process exit for errors, or verify the
base snapshot is version 1; a stopped process alone does not prove checkpoint
success. The agent CLI propagates checkpoint failures in its exit status. After a forced termination, first open and close
with the new version before downgrading. `Checkpoint()` makes the same compatible
snapshot without sealing the store; the next mutation upgrades the marker again.
Never delete a journal to force an older binary to start.

`Message` has stable identity fields `ID`, `DeviceID`, `Direction`, `Kind`,
`Text`, `Name`, and `Created`. Reusing an ID with different identity returns
`ErrConflict`, even when the revision is stale. A matching message with the
same or older revision is an idempotent no-op. Newer revisions replace mutable
progress, status, transfer metadata, and error fields. `MarkSynced(id, revision)`
monotonically acknowledges an existing revision without changing the message's
wire revision. An acknowledgement cannot exceed the current revision.

Messages are bounded to 20,000 records, text to 4,096 Unicode code points,
names to 255, file sizes to 1 TiB, and byte progress to `[0,size]`. `Path` is
empty or a contained relative inbox path. It is never an arbitrary local path.
`List()` returns value copies in creation order, with ID as the tie-breaker.
The creation-order index is maintained on insertion. `JSON(version)` serves a
cached immutable snapshot or `{version, unchanged:true}` for an unchanged poll;
versions include a fresh store-instance nonce so restart cannot reuse a cached
client history. `ActiveIncoming(now)` scans only active received attachments,
and `WorkList()` excludes incoming history and acknowledged outgoing text.

The engine is responsible for authenticated device transport, deriving the
incoming device identity, stripping local fields such as `SyncedRevision`
before transport, deciding allowed state transitions, and managing file bytes.
This package performs no network operations and does not open inbox files.

Run `go test -race ./internal/chat` to exercise persistence, immutable identity,
replay idempotency, path/size bounds, atomic failure behavior, and concurrent
revision updates.
