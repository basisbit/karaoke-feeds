#!/usr/bin/env python3
"""THROWAWAY PROTOTYPE of a karaoke-feed generator (SPEC.md v0.2).

Not the real tool (that one will be Go). Purpose: test the spec against real data.
Stateless: reads a songs folder, writes a complete feed. Signing uses the openssl CLI (Ed25519).
Usage: gen_feed.py <songs_dir> <out_dir> --id <catalog id> --name <catalog name>
                   [--key private.pem ...] [--pub ed25519:<b64url> ...] [--sequence N] [--removed id ...] [--plain]
Optional per-folder `feed-meta.json`: {"licenses": {"audio": {spdx,url,attribution,source}, "*": {...}}, "age": 12}
"""
import argparse, base64, gzip, hashlib, json, os, re, shutil, subprocess, time, unicodedata
from datetime import datetime, timezone

SPEC = "karaoke-feed/0.2"
MEDIA_HEADERS = {"MP3": "audio", "AUDIO": "audio", "VOCALS": "vocals", "INSTRUMENTAL": "instrumental",
                 "VIDEO": "video", "COVER": "cover", "BACKGROUND": "background"}
MIME = {".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".opus": "audio/opus", ".m4a": "audio/mp4",
        ".mp4": "video/mp4", ".webm": "video/webm", ".avi": "video/x-msvideo", ".mkv": "video/x-matroska",
        ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".txt": "text/plain"}
SECTION_ROLES = [(re.compile(r"^(mp3|audio|ogg)\b.*file\s*:", re.I), "audio"),
                 (re.compile(r"^cover\b.*:", re.I), "cover"),
                 (re.compile(r"^background\b.*:", re.I), "background"),
                 (re.compile(r"^video\b.*:", re.I), "video"),
                 (re.compile(r"^ultrastar.*(txt|song).*:", re.I), "txt")]
NOLICENSE = {"spdx": "NOLICENSE"}
BAD_CHARS = re.compile(r'[/\\:*?"<>|\x00-\x1f\x7f]')
DEVICE = re.compile(r"^(con|prn|aux|nul|com[0-9]|lpt[0-9])$", re.I)
warnings = []


def name_ok(n):
    """SPEC 5.2 rules 1-4 (extension and uniqueness are checked by the caller)."""
    return (0 < len(n.encode()) <= 160 and not BAD_CHARS.search(n) and n[0] not in ". " and n[-1] not in ". "
            and not DEVICE.match(n.split(".", 1)[0]))


def b64u(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def public_key(pem):
    der = subprocess.run(["openssl", "pkey", "-in", pem, "-pubout", "-outform", "DER"], check=True, capture_output=True).stdout
    return "ed25519:" + b64u(der[-32:])


def sign(pem, path):
    return subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", pem, "-rawin", "-in", path], check=True, capture_output=True).stdout


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def decode(data):
    for enc in ("utf-8-sig", "cp1252"):
        try:
            return data.decode(enc), enc
        except UnicodeDecodeError:
            pass
    return data.decode("latin-1"), "latin-1"


def slug(s):
    s = unicodedata.normalize("NFKD", s).encode("ascii", "ignore").decode().lower()
    return re.sub(r"[^a-z0-9]+", "-", s).strip("-")[:56] or "song"


def spdx_from_url(url):
    u = url.lower()
    m = re.search(r"creativecommons\.org/licenses/([a-z-]+)/(\d\.\d)", u)
    if m:
        return f"CC-{m.group(1).upper()}-{m.group(2)}"
    if "publicdomain/zero" in u or "/cc0" in u:
        return "CC0-1.0"
    if "public-domain" in u or "publicdomain" in u:
        return "LicenseRef-PublicDomain"
    return "NOLICENSE"


def clean(lic):
    return {k: v for k, v in lic.items() if v}


def parse_license(text):
    """Return (per_role dict, fallback dict). Heuristic for the free-text license.txt of the USDX songs repo."""
    per_role, cur, block = {}, None, []

    def flush():
        if cur and block:
            url = next((re.search(r"https?://\S+", l).group(0) for l in block if re.match(r"\s*licen[cs]e\s*:", l, re.I)
                        and re.search(r"https?://\S+", l)), None)
            src = next((l.split(":", 1)[1].strip() for l in block if re.match(r"\s*file found on\s*:", l, re.I)), None)
            attr = next((l.strip() for l in block if l.strip() and not re.match(r"\s*(licen[cs]e|file found on|contact)\s*:", l, re.I)), None)
            if url and spdx_from_url(url) != "NOLICENSE":
                per_role[cur] = clean({"spdx": spdx_from_url(url), "url": url, "attribution": attr, "source": src})

    for line in text.splitlines():
        role = next((r for rx, r in SECTION_ROLES if rx.match(line.strip())), None)
        if role:
            flush()
            cur, block = role, []
        elif cur:
            block.append(line)
    flush()
    found = [u.rstrip("/.)") for u in re.findall(r"https?://(?:www\.)?creativecommons\.org/[^\s\]\)]+", text)]
    urls = sorted({u for u in found if "/licenses/" in u or "publicdomain" in u or "public-domain" in u})
    spdx = sorted({spdx_from_url(u) for u in urls})
    if len(spdx) == 1 and spdx[0] != "NOLICENSE":
        return per_role, {"spdx": spdx[0], "url": urls[0], "attribution": "see license.txt"}
    return per_role, dict(NOLICENSE)  # several or no licenses found: needs operator curation (feed-meta.json)


def parse_txt(text):
    headers, voices, last_beat = {}, set(), 0
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("#") and ":" in line:
            k, v = line[1:].split(":", 1)
            headers.setdefault(k.strip().upper(), v.strip())
        elif re.match(r"^P\s*\d", line):
            voices.add(line.replace(" ", ""))
        else:
            m = re.match(r"^[:*FRG]\s+(-?\d+)\s+(\d+)", line)
            if m:
                last_beat = max(last_beat, int(m.group(1)) + int(m.group(2)))
    return headers, max(1, len(voices)), last_beat


def num(s):
    try:
        return float(s.replace(",", "."))
    except (AttributeError, ValueError):
        return None


def multi(v):
    return [x.strip() for x in v.split(",") if x.strip()] if v else []


def build_entry(folder, fnames, txt_name, lic, meta, used_ids, blobs, licenses):
    raw = open(os.path.join(folder, txt_name), "rb").read()
    text, enc = decode(raw)
    h, voices, last_beat = parse_txt(text)
    if enc != "utf-8-sig" or raw.startswith(b"\xef\xbb\xbf"):
        warnings.append(f"{txt_name}: encoding {enc}{' with BOM' if raw.startswith(b'\xef\xbb\xbf') else ''} (bytes published unchanged)")
    artist, title = h.get("ARTIST", ""), h.get("TITLE", "")
    base_id = slug(f"{artist}-{title}")
    sid, n = base_id, 2
    while sid in used_ids:
        sid, n = f"{base_id}-{n}", n + 1
    used_ids.add(sid)
    per_role, fallback = lic
    on_disk = {unicodedata.normalize("NFC", f).lower(): f for f in fnames}
    files, ok = [], True

    def add(role, name):
        nonlocal ok
        if unicodedata.normalize("NFC", name) != name or not name_ok(name):
            warnings.append(f"{sid}: {role} reference is not a safe NFC file name, entry skipped: {name!r}")
            ok = False
            return
        real = name if name in fnames else on_disk.get(name.lower())
        if real is None:
            warnings.append(f"{sid}: referenced {role} file missing: {name}")
            return
        if real != name:
            warnings.append(f"{sid}: {role} reference differs from the file on disk only by case/normalization; published under the txt's spelling")
        if name.lower() in {f["name"].lower() for f in files}:
            return
        fp = os.path.join(folder, real)
        digest = sha256_file(fp)
        blobs[digest] = fp
        f = {"role": role, "name": name, "sha256": "sha256:" + digest, "size": os.path.getsize(fp),
             "type": MIME.get(os.path.splitext(name)[1].lower(), "application/octet-stream")}
        if role != "license":
            l = (meta.get("licenses") or {}).get(role) or (meta.get("licenses") or {}).get("*") or per_role.get(role) or fallback
            f["license"] = l
            licenses.add(json.dumps(l, sort_keys=True))
        files.append(f)

    add("txt", txt_name)
    for key, role in MEDIA_HEADERS.items():
        if h.get(key) and not (role == "audio" and any(f["role"] == "audio" for f in files)):
            add(role, h[key])
    lic_name = next((f for f in fnames if f.lower() == "license.txt"), None)
    if lic_name:
        add("license", lic_name)
    if not ok:
        return None
    bpm, gap = num(h.get("BPM")), num(h.get("GAP")) or 0
    year = num(h.get("YEAR"))
    entry = {"id": sid, "artist": artist, "title": title, "rendition": h.get("RENDITION"),
             "language": multi(h.get("LANGUAGE")), "year": int(year) if year else None, "genre": multi(h.get("GENRE")),
             "edition": h.get("EDITION"), "tags": multi(h.get("TAGS")), "creator": multi(h.get("CREATOR")),
             "age": meta.get("age"), "duration": round(gap / 1000 + last_beat * 60 / (bpm * 4), 1) if bpm else None,
             "voices": voices, "preview_start": num(h.get("PREVIEWSTART"))}
    return {k: v for k, v in entry.items() if v not in (None, [], "")} | {"files": files}


def generate(songs_dir, out_dir, cat_id, cat_name, keys=(), pubs=(), sequence=None, removed=(), plain=False,
             feed_name="UltraStar Deluxe CC Songs (prototype)"):
    entries, used_ids, blobs, licenses = [], set(), {}, set()
    for root, dirs, fnames in sorted(os.walk(songs_dir)):
        dirs.sort()
        lic_name = next((f for f in fnames if f.lower() == "license.txt"), None)
        lic = parse_license(decode(open(os.path.join(root, lic_name), "rb").read())[0]) if lic_name else ({}, dict(NOLICENSE))
        meta = json.load(open(os.path.join(root, "feed-meta.json"), encoding="utf-8")) if "feed-meta.json" in fnames else {}
        for f in sorted(fnames):
            if f.lower().endswith(".txt") and f != lic_name:
                e = build_entry(root, fnames, f, lic, meta, used_ids, blobs, licenses)
                if e:
                    entries.append(e)
    for digest, src in blobs.items():
        dst = os.path.join(out_dir, "blobs", digest[:2], digest)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        if not os.path.exists(dst):
            try:
                os.link(src, dst)
            except OSError:
                shutil.copyfile(src, dst)
    catalog = {"spec": SPEC, "id": cat_id, "songs": entries, "moved": {}}
    raw_json = json.dumps(catalog, ensure_ascii=False, separators=(",", ":")).encode()
    cat_bytes = raw_json if plain else gzip.compress(raw_json, 9, mtime=0)
    cat_hex = hashlib.sha256(cat_bytes).hexdigest()
    cat_path = f"catalog/{cat_hex}.json" + ("" if plain else ".gz")
    os.makedirs(os.path.join(out_dir, "catalog"), exist_ok=True)
    open(os.path.join(out_dir, cat_path), "wb").write(cat_bytes)
    now = int(time.time())
    feed = {"spec": SPEC, "name": feed_name,
            "description": "Prototype feed generated from a songs folder",
            "homepage": "https://github.com/UltraStar-Deluxe/songs",
            "operator": {"name": "PROTOTYPE - not an official feed", "contact": "mailto:copyright@usdx.eu"},
            "sequence": sequence if sequence is not None else now,
            "generated": datetime.fromtimestamp(now, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}
    all_keys = [public_key(k) for k in keys] + list(pubs)
    if all_keys:
        feed["keys"] = all_keys
    if removed:
        feed["removed"] = sorted(removed)
    feed["catalogs"] = [{"id": cat_id, "names": {"en": cat_name}, "path": cat_path, "sha256": "sha256:" + cat_hex,
                         "size": len(cat_bytes), "songs": len(entries),
                         "bytes": sum(os.path.getsize(p) for p in blobs.values())}]
    feed_bytes = (json.dumps(feed, ensure_ascii=False, indent=2) + "\n").encode()
    feed_path = os.path.join(out_dir, "feed.json")
    tmp = feed_path + ".tmp"
    open(tmp, "wb").write(feed_bytes)
    if keys:  # publishing order: blobs, catalog, signature, feed.json last
        os.makedirs(os.path.join(out_dir, "sig"), exist_ok=True)
        open(os.path.join(out_dir, "sig", hashlib.sha256(feed_bytes).hexdigest() + ".sig"), "w").write(b64u(sign(keys[0], tmp)) + "\n")
    os.replace(tmp, feed_path)
    return {"entries": len(entries), "file_refs": sum(len(e["files"]) for e in entries), "unique_blobs": len(blobs),
            "blob_bytes": feed["catalogs"][0]["bytes"], "catalog_json_bytes": len(raw_json), "catalog_stored_bytes": len(cat_bytes),
            "distinct_licenses": len(licenses), "sequence": feed["sequence"], "keys": all_keys, "catalog": catalog}


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("songs_dir"); ap.add_argument("out_dir")
    ap.add_argument("--id", required=True); ap.add_argument("--name", required=True)
    ap.add_argument("--key", action="append", default=[]); ap.add_argument("--pub", action="append", default=[])
    ap.add_argument("--sequence", type=int); ap.add_argument("--removed", action="append", default=[])
    ap.add_argument("--plain", action="store_true", help="write catalog as .json instead of .json.gz")
    a = ap.parse_args()
    r = generate(a.songs_dir, a.out_dir, a.id, a.name, a.key, a.pub, a.sequence, a.removed, a.plain)
    cat = r.pop("catalog")
    print(r)
    lic = {}
    for e in cat["songs"]:
        for f in e["files"]:
            if "license" in f:
                s = f["license"]["spdx"]
                lic[s] = lic.get(s, 0) + 1
    print("licenses per file:", dict(sorted(lic.items(), key=lambda x: -x[1])))
    print("files without source:", sum(1 for e in cat["songs"] for f in e["files"] if "license" in f and "source" not in f["license"]))
    print(f"warnings ({len(warnings)}):")
    for w in warnings:
        print("  -", w)
