#!/usr/bin/env python3
"""THROWAWAY PROTOTYPE client for SPEC.md v0.2: fetch a feed, verify, sync songs into a library folder.
Usage: client_test.py <feed.json URL (http://, https:// or file://)[#ed25519=<key>]> <library_dir> [max_songs]
State lives in <library_dir>/.subscription.json and .feed.json (the last accepted feed.json).
Signature checks use the openssl CLI.
"""
import base64, hashlib, json, os, re, subprocess, sys, tempfile, urllib.error, urllib.request, zlib

SPEC = "karaoke-feed/0.2"
BAD_CHARS = re.compile(r'[/\\:*?"<>|\x00-\x1f\x7f]')
DEVICE = re.compile(r"^(con|prn|aux|nul|com[0-9]|lpt[0-9])$", re.I)
AUDIO = {".mp3", ".ogg", ".opus", ".m4a", ".flac", ".wav"}
IMAGE = {".jpg", ".jpeg", ".png", ".webp"}
EXT = {"txt": {".txt"}, "audio": AUDIO, "vocals": AUDIO, "instrumental": AUDIO, "preview": AUDIO,
       "video": {".mp4", ".webm", ".mkv", ".avi", ".mpg", ".mov"}, "cover": IMAGE, "background": IMAGE,
       "license": {".txt", ".md"}}
ED25519_DER_PREFIX = bytes.fromhex("302a300506032b6570032100")


class Reject(Exception):
    pass


def get(url, limit):
    """Download at most `limit` bytes; more than that is an error (SPEC 5.1.5)."""
    req = urllib.request.Request(url, headers={"User-Agent": "karaoke-feed-proto"})
    with urllib.request.urlopen(req, timeout=30) as r:
        data = r.read(limit + 1)
    if len(data) > limit:
        raise Reject(f"more than the declared {limit} bytes")
    return data


def sha(data):
    return hashlib.sha256(data).hexdigest()


def b64u_decode(s):
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def sig_ok(key, message, sig_text):
    try:
        pub, sig = b64u_decode(key.split(":", 1)[1]), b64u_decode(sig_text.strip())
    except Exception:
        return False
    if not key.startswith("ed25519:") or len(pub) != 32 or len(sig) != 64:
        return False
    with tempfile.TemporaryDirectory() as t:
        for n, b in (("pub", ED25519_DER_PREFIX + pub), ("msg", message), ("sig", sig)):
            open(os.path.join(t, n), "wb").write(b)
        return subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", t + "/pub", "-keyform", "DER",
                               "-rawin", "-in", t + "/msg", "-sigfile", t + "/sig"], capture_output=True).returncode == 0


def name_ok(name, role):
    """SPEC 5.2 rules 1-5."""
    return (isinstance(name, str) and 0 < len(name.encode()) <= 160 and not BAD_CHARS.search(name)
            and name[0] not in ". " and name[-1] not in ". " and not DEVICE.match(name.split(".", 1)[0])
            and os.path.splitext(name)[1].lower() in EXT.get(role, ()))


def gunzip(data, limit=64 << 20):
    d = zlib.decompressobj(31)
    out = d.decompress(data, limit + 1)
    if len(out) > limit or not d.eof:
        raise Reject("catalog too large or not a single gzip member")
    return out


def accept_feed(url, base, lib, state, log):
    """SPEC 5.1 steps 1-2 and section 6. Returns the feed.json to use (new or cached)."""
    cached = os.path.join(lib, ".feed.json")
    try:
        raw = get(url, 1 << 20)
        feed = json.loads(raw)
        if feed.get("spec") != SPEC:
            raise Reject(f"unsupported spec {feed.get('spec')!r}")
        trust = state["trust"] or (feed.get("keys", []) if state["sequence"] is None else [])  # pinned, or trust on first use
        if trust:
            if not 1 <= len(feed.get("keys", [])) <= 8:
                raise Reject("signed subscription but feed.json has no keys")
            try:
                sig = get(base + f"sig/{sha(raw)}.sig", 1024).decode("ascii", "replace")
            except (urllib.error.URLError, OSError):
                raise Reject("signature file missing")
            if not any(sig_ok(k, raw, sig) for k in trust):
                raise Reject("signature invalid")
        if state["sequence"] is not None and (feed["sequence"] < state["sequence"]
                                              or (feed["sequence"] == state["sequence"] and sha(raw) != state["feed_sha"])):
            raise Reject(f"rollback: sequence {feed['sequence']} is not newer than {state['sequence']}")
        if trust:
            state["trust"] = feed["keys"]  # key rotation
        state["sequence"], state["feed_sha"] = feed["sequence"], sha(raw)
        open(cached, "wb").write(raw)
        log["feed"] = "accepted"
        return feed
    except (Reject, urllib.error.URLError, OSError, ValueError, KeyError) as e:
        log["feed"] = f"ignored ({e})"
        if not os.path.exists(cached):
            raise Reject(f"no usable feed.json: {e}")
        return json.load(open(cached, encoding="utf-8"))  # keep using the last accepted one; location still serves blobs


def sync(feed_url, lib, max_songs=None, show_undeclared=False, verbose=False):
    url, _, frag = feed_url.partition("#")
    base = url.rsplit("/", 1)[0] + "/"
    os.makedirs(lib, exist_ok=True)
    spath = os.path.join(lib, ".subscription.json")
    state = json.load(open(spath)) if os.path.exists(spath) else {
        "trust": ["ed25519:" + frag[8:]] if frag.startswith("ed25519=") else [], "sequence": None, "feed_sha": None, "songs": {}}
    log = {"synced": [], "unchanged": [], "skipped_unsafe": [], "hidden_undeclared": [], "hidden_absent": [],
           "withdrawn": [], "kept_modified": [], "failed": [], "moved": []}
    feed = accept_feed(url, base, lib, state, log)
    signed = log["signed"] = bool(state["trust"])
    listed, removed, moved = {}, set(feed.get("removed", [])), {}
    for cat in feed["catalogs"]:
        try:
            craw = get(base + cat["path"], cat["size"])
            if len(craw) != cat["size"] or "sha256:" + sha(craw) != cat["sha256"]:
                raise Reject("catalog hash mismatch")
            catalog = json.loads(gunzip(craw) if cat["path"].endswith(".gz") else craw)
        except (Reject, urllib.error.URLError, OSError, ValueError) as e:
            log["failed"].append(f"catalog {cat['id']}: {e}")
            continue
        for s in catalog["songs"]:
            if s["id"] not in listed:  # an id in two catalogs: first one wins
                listed[s["id"]] = s
        moved.update(catalog.get("moved", {}))

    removed -= set(listed)  # listed wins
    for old, new in moved.items():
        if old in state["songs"] and new not in state["songs"]:
            state["songs"][new] = state["songs"].pop(old)
            log["moved"].append(f"{old}->{new}")

    for sid in [i for i in state["songs"] if i not in listed]:
        rec = state["songs"][sid]
        folder = os.path.join(lib, rec["folder"])
        if sid in removed and signed:  # withdrawn by the operator: delete unmodified cached files
            kept = False
            for name, h in rec["files"].items():
                p = os.path.join(folder, name)
                if os.path.exists(p):
                    if sha(open(p, "rb").read()) == h:
                        os.remove(p)
                    else:
                        kept = True
                        log["kept_modified"].append(f"{sid}/{name}")
            sidecar = os.path.join(folder, ".feed-song.json")
            if not kept and os.path.exists(sidecar):
                os.remove(sidecar)
            if os.path.isdir(folder) and not os.listdir(folder):
                os.rmdir(folder)
            del state["songs"][sid]
            log["withdrawn"].append(sid)
        else:  # merely absent (or unsigned feed): hide, keep files
            rec["hidden"] = True
            log["hidden_absent"].append(sid)

    for sid, s in list(listed.items())[:max_songs]:
        files = [f for f in s["files"] if f.get("role") in EXT]
        names = [f.get("name") for f in files]
        if (not re.fullmatch(r"[a-z0-9][a-z0-9._-]{0,63}", sid) or sum(f["role"] == "txt" for f in files) != 1
                or not all(name_ok(f.get("name"), f["role"]) for f in files) or len({n.lower() for n in names}) != len(names)):
            log["skipped_unsafe"].append(sid)
            continue
        undeclared = any((f.get("license") or {}).get("spdx", "NOLICENSE") == "NOLICENSE"
                         for f in files if f["role"] != "license")
        if undeclared and not show_undeclared:
            log["hidden_undeclared"].append(sid)
            continue
        want = {f["name"]: f["sha256"].split(":", 1)[1] for f in files}
        rec = state["songs"].get(sid)
        if rec and rec["files"] == want and not rec.get("hidden"):
            log["unchanged"].append(sid)
            continue
        folder = os.path.join(lib, rec["folder"] if rec else sid)
        os.makedirs(folder, exist_ok=True)
        ok = True
        for f in files:
            hexd, p = want[f["name"]], os.path.join(folder, f["name"])
            if os.path.exists(p):
                on_disk = sha(open(p, "rb").read())
                if on_disk == hexd:
                    continue
                if rec and on_disk != rec["files"].get(f["name"]):  # changed locally: never overwrite without asking
                    log["kept_modified"].append(f"{sid}/{f['name']}")
                    continue
            try:
                data = get(base + f"blobs/{hexd[:2]}/{hexd}", f["size"])
                if len(data) != f["size"] or sha(data) != hexd:
                    raise Reject("hash mismatch")
            except (Reject, urllib.error.URLError, OSError) as e:
                log["failed"].append(f"{sid}/{f['name']}: {e}")
                ok = False
                continue
            open(p + ".part", "wb").write(data)
            os.replace(p + ".part", p)
        if ok:
            if rec:  # files the song no longer has: delete if unmodified
                for name, h in rec["files"].items():
                    p = os.path.join(folder, name)
                    if name not in want and os.path.exists(p) and sha(open(p, "rb").read()) == h:
                        os.remove(p)
            state["songs"][sid] = {"folder": os.path.basename(folder), "files": want}
            json.dump({"feed": url, "id": sid, "files": want}, open(os.path.join(folder, ".feed-song.json"), "w"))
            log["synced"].append(sid)
    json.dump(state, open(spath, "w"), indent=1)
    if verbose:
        print(f"feed '{feed['name']}' seq={feed['sequence']} contact={feed['operator']['contact']}")
        for k, v in log.items():
            print(f"  {k}: {v if not isinstance(v, list) else (len(v), v[:6])}")
    return log


if __name__ == "__main__":
    try:
        sync(sys.argv[1], sys.argv[2], int(sys.argv[3]) if len(sys.argv) > 3 else None, verbose=True)
    except Reject as e:
        sys.exit(f"REJECTED: {e}")
