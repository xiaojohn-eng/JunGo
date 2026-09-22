#!/usr/bin/env python3
"""Resume a private 5 GiB QA checkpoint with isolated native loopback agents.

No credentials are printed. Snapshot contents are cloned, never changed. Run
--prepare-only before a native build is ready, then reuse the reported --work-dir.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
os.umask(0o077)


def read_json(path):
    return json.loads(Path(path).read_text())


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("w") as f:
        json.dump(value, f, ensure_ascii=False, indent=2)
        f.flush()
        os.fsync(f.fileno())
    temporary.chmod(0o600)
    temporary.replace(path)


def digest(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as f:
        for block in iter(lambda: f.read(4 << 20), b""):
            h.update(block)
    return h.hexdigest()


def prepare(snapshot, work):
    manifest_path = work / "manifest.json"
    if manifest_path.exists():
        return read_json(manifest_path)
    if work.exists() and any(work.iterdir()):
        raise RuntimeError("work directory is not an existing QA run or empty directory")
    work.mkdir(parents=True, exist_ok=True, mode=0o700)
    work.chmod(0o700)
    data = work / "data"
    # APFS copy-on-write preserves the sparse 5 GiB source and keeps the
    # original durable checkpoint available for a separate retry.
    if sys.platform == "darwin":
        subprocess.run(["/bin/cp", "-cR", str(snapshot), str(data)], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    else:
        shutil.copytree(snapshot, data)
    a, b = data / "001/a", data / "001/b"
    tasks = read_json(a / "transfers.json")
    uploads = [t for t in tasks if t.get("direction") == "upload" and t.get("uploadId")]
    if len(uploads) != 1:
        raise RuntimeError("expected one recoverable checkpoint upload")
    upload = uploads[0]
    old_root = Path(upload["source"]).parent.parent

    def remap(value):
        if not value:
            return value
        try:
            return str(data / Path(value).relative_to(old_root))
        except ValueError:
            raise RuntimeError("checkpoint path is outside the original QA fixture")

    for directory in (a, b):
        config = read_json(directory / "device.json")
        for share in config.get("shares", []):
            share["path"] = remap(share["path"])
            Path(share["path"]).mkdir(parents=True, exist_ok=True, mode=0o700)
        write_json(directory / "device.json", config)
        (directory / "local-api.json").unlink(missing_ok=True)
    for task in tasks:
        for key in ("source", "destination"):
            if task.get(key):
                task[key] = remap(task[key])
        if task.get("status") not in ("complete", "cancelled"):
            task["status"] = "paused"
    write_json(a / "transfers.json", tasks)
    controller = work / "control"
    controller.mkdir(mode=0o700)
    shutil.copy2(data / "001/control.json", controller / "registry.json")
    source = Path(upload["source"])
    if source.stat().st_size != 5 << 30:
        raise RuntimeError("checkpoint source is not exactly 5 GiB")
    expected = digest(source)
    if expected != upload["sha256"]:
        raise RuntimeError("checkpoint source checksum differs from the saved transfer")
    remote = read_json(b / "files" / (upload["uploadId"] + ".json"))
    if remote["offset"] != upload["completed"] or remote["sha256"] != expected:
        raise RuntimeError("saved client and server confirmed checkpoints disagree")
    manifest = {
        "sender": str(a), "receiver": str(b), "controller": str(controller),
        "source": str(source), "destination": str(work / "download-five-gib.bin"),
        "uploadTask": upload["id"], "uploadID": upload["uploadId"],
        "peerID": upload["deviceId"], "shareID": upload["shareId"],
        "checkpointBytes": remote["offset"], "size": 5 << 30, "sha256": expected,
    }
    write_json(manifest_path, manifest)
    return manifest


class RecoveryQA:
    def __init__(self, binary, work, manifest, minutes):
        self.binary, self.work, self.manifest = binary, work, manifest
        self.children, self.logs = [], []
        self.started = time.monotonic()
        self.deadline = self.started + minutes * 60
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.progress = (work / "progress.jsonl").open("a", buffering=1)
        self.paths_seen = set()

    def event(self, stage, **fields):
        entry = {"time": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "stage": stage,
                 "elapsedSeconds": round(time.monotonic()-self.started, 1), **fields}
        line = json.dumps(entry, ensure_ascii=False)
        print(line, flush=True)
        self.progress.write(line + "\n")

    def check(self):
        if time.monotonic() >= self.deadline:
            raise TimeoutError("25-minute QA budget exhausted; durable progress remains in the run directory")
        if any(p.poll() is not None for p in self.children):
            raise RuntimeError("an owned QA process exited; inspect private process logs")

    def start(self, name, args):
        log = (self.work / (name + ".log")).open("a")
        self.logs.append(log)
        child = subprocess.Popen([str(self.binary), *args], stdout=log, stderr=log,
                                 start_new_session=True)
        self.children.append(child)
        return child

    def rpc(self, directory, method, params=None):
        self.check()
        info = read_json(Path(directory) / "local-api.json")
        if not info["url"].startswith("http://127.0.0.1:"):
            raise RuntimeError("local RPC did not bind loopback")
        request = urllib.request.Request(info["url"] + "/v1/rpc",
            json.dumps({"method": method, "params": params or {}}).encode(),
            {"Authorization": "Bearer " + info["token"], "Content-Type": "application/json"})
        with self.opener.open(request, timeout=30) as response:
            return json.load(response)

    def state(self):
        state = self.rpc(self.manifest["sender"], "state")
        for peer in state.get("peers", []):
            if peer["id"] == self.manifest["peerID"]:
                self.paths_seen.add(peer.get("path", "unavailable"))
        return state

    def wait_agents(self):
        until = time.monotonic() + 90
        while time.monotonic() < until:
            self.check()
            try:
                a = self.state()
                b = self.rpc(self.manifest["receiver"], "state")
                if a["meshRunning"] and b["meshRunning"] and any(
                    p["id"] == self.manifest["peerID"] and p.get("file_tls_fingerprint")
                    for p in a["peers"]
                ):
                    return
            except (OSError, ValueError, KeyError, urllib.error.URLError):
                pass
            time.sleep(.25)
        raise TimeoutError("isolated agents did not become reachable")

    def wait_transfer(self, identifier, stage):
        next_report = 0
        while True:
            state = self.state()
            transfer = next((t for t in state["transfers"] if t["id"] == identifier), None)
            if transfer is None:
                raise RuntimeError("QA transfer disappeared")
            now = time.monotonic()
            if now >= next_report or transfer["status"] in ("complete", "failed"):
                self.event(stage, status=transfer["status"], completedBytes=transfer["completed"],
                    totalBytes=transfer["size"], paths=sorted(self.paths_seen))
                next_report = now + 30
            if transfer["status"] == "failed":
                raise RuntimeError("QA transfer failed: " + transfer.get("error", "unspecified"))
            if transfer["status"] == "complete":
                return transfer
            time.sleep(2)

    def run(self):
        m = self.manifest
        controller = Path(m["controller"])
        with socket.socket() as reserve:
            reserve.bind(("127.0.0.1", 0))
            port = reserve.getsockname()[1]
        base = "https://127.0.0.1:" + str(port)
        self.start("control", ["serve", "--state", str(controller), "--listen", "127.0.0.1:" + str(port), "--stun", ""])
        cert = controller / "tls/identity.crt"
        until = time.monotonic() + 20
        while time.monotonic() < until:
            self.check()
            if cert.exists():
                try:
                    # macOS system Python may use LibreSSL without TLS 1.3.
                    # This is only a listener-ready probe; the native agents
                    # below perform pinned TLS 1.3 and authenticated enrollment.
                    with socket.create_connection(("127.0.0.1", port), timeout=2):
                        pass
                    break
                except OSError:
                    pass
            time.sleep(.1)
        else:
            raise TimeoutError("local TLS control service did not start")
        fingerprint = hashlib.sha256(ssl.PEM_cert_to_DER_cert(cert.read_text())).hexdigest()
        for name, directory in (("receiver", m["receiver"]), ("sender", m["sender"])):
            directory = Path(directory)
            config = read_json(directory / "device.json")
            if config["serviceId"] != read_json(controller / "registry.json")["service_id"]:
                raise RuntimeError("copied device belongs to a different control service")
            config["server"], config["fingerprint"] = base, fingerprint
            config["meshEnabled"] = True
            write_json(directory / "device.json", config)
            (directory / "local-api.json").unlink(missing_ok=True)
            self.start(name, ["agent", "--state", str(directory), "--local-api"])
        self.wait_agents()
        recovered = read_json(Path(m["receiver"]) / "files" / (m["uploadID"] + ".json"))
        if recovered["state"] == "pending":
            confirmed = recovered["offset"]
            durable_size = (Path(m["receiver"]) / "files" / (m["uploadID"] + ".part")).stat().st_size
            if confirmed != durable_size:
                raise RuntimeError("receiver did not discard the unconfirmed upload tail")
            self.event("restored", checkpointBytes=m["checkpointBytes"], serverConfirmedBytes=confirmed,
                       serverPartBytes=durable_size, totalBytes=m["size"])
        else:
            self.event("restored", checkpointBytes=m["checkpointBytes"], remoteStatus=recovered["state"], totalBytes=m["size"])
        upload = next(t for t in self.state()["transfers"] if t["id"] == m["uploadTask"])
        if upload["status"] != "complete":
            self.rpc(m["sender"], "transferAction", {"id": upload["id"], "action": "resume"})
        upload = self.wait_transfer(upload["id"], "upload")
        remote = read_json(Path(m["receiver"]) / "files" / (m["uploadID"] + ".json"))
        if upload["uploadId"] != m["uploadID"] or remote["state"] != "completed" or remote["offset"] != m["size"]:
            raise RuntimeError("upload did not continue the original confirmed remote task")
        config = read_json(Path(m["receiver"]) / "device.json")
        share = next(s for s in config["shares"] if s["id"] == m["shareID"])
        uploaded = Path(share["path"]) / upload["path"]
        if uploaded.stat().st_size != m["size"] or digest(uploaded) != m["sha256"]:
            raise RuntimeError("completed upload checksum differs")
        self.event("upload_verified", totalBytes=m["size"], sha256=m["sha256"])
        if not m.get("downloadTask"):
            task = self.rpc(m["sender"], "download", {"deviceId": m["peerID"], "shareId": m["shareID"],
                "path": upload["path"], "destination": m["destination"]})
            m["downloadTask"] = task["id"]
            write_json(self.work / "manifest.json", m)
        else:
            task = next(t for t in self.state()["transfers"] if t["id"] == m["downloadTask"])
            if task["status"] not in ("complete", "running", "hashing", "queued", "waiting"):
                self.rpc(m["sender"], "transferAction", {"id": task["id"], "action": "resume"})
        downloaded = self.wait_transfer(m["downloadTask"], "download")
        destination = Path(m["destination"])
        if downloaded["completed"] != m["size"] or destination.stat().st_size != m["size"] or digest(destination) != m["sha256"]:
            raise RuntimeError("completed download checksum differs")
        result = {"status": "passed", "bytesEachDirection": m["size"], "uploadResumedAt": m["checkpointBytes"],
                  "sha256": m["sha256"], "pathsObserved": sorted(self.paths_seen),
                  "elapsedSeconds": round(time.monotonic()-self.started, 1)}
        write_json(self.work / "result.json", result)
        self.event("complete", **result)

    def close(self):
        for child in reversed(self.children):
            if child.poll() is None:
                child.terminate()
        for child in reversed(self.children):
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        for log in self.logs:
            log.close()
        self.progress.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", type=Path, default=ROOT / ".state/large-recovery/snapshot")
    parser.add_argument("--work-dir", type=Path)
    parser.add_argument("--binary", type=Path, default=ROOT / "dist/jungo")
    parser.add_argument("--prepare-only", action="store_true")
    parser.add_argument("--minutes", type=int, default=25)
    args = parser.parse_args()
    work = (args.work_dir or ROOT / ".state/large-recovery" / time.strftime("run-%Y%m%d-%H%M%S")).resolve()
    manifest = prepare(args.snapshot.resolve(), work)
    if args.prepare_only:
        print(json.dumps({"stage": "prepared", "workDir": str(work), "checkpointBytes": manifest["checkpointBytes"], "totalBytes": manifest["size"]}), flush=True)
        return
    if not args.binary.is_file():
        raise RuntimeError("native QA binary is not ready")
    qa = RecoveryQA(args.binary.resolve(), work, manifest, args.minutes)
    def interrupt(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    try:
        qa.run()
    except BaseException as err:
        result = {"status": "interrupted" if isinstance(err, KeyboardInterrupt) else "failed",
                  "reason": str(err) or "signal", "elapsedSeconds": round(time.monotonic()-qa.started, 1)}
        write_json(work / "result.json", result)
        qa.event("stopped", **result)
        raise
    finally:
        qa.close()


if __name__ == "__main__":
    main()
