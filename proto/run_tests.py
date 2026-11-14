#!/usr/bin/env python3
"""THROWAWAY tests for the SPEC v0.2 prototypes. Builds a synthetic songs folder (dummy bytes, no real content)
with the edge cases found in real libraries, then runs generator + client over file://.
Usage: run_tests.py <empty work dir>
"""
import gzip, hashlib, json, os, shutil, subprocess, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gen_feed as gen
import client_test as cli

W = os.path.abspath(sys.argv[1])
SONGS, OUT = os.path.join(W, "songs"), os.path.join(W, "feed")
URL = "file://" + OUT + "/feed.json"
BY = {"licenses": {"*": {"spdx": "CC-BY-4.0", "url": "https://creativecommons.org/licenses/by/4.0/",
                         "attribution": "Test Artist", "source": "https://example.org/src"}}}
passed = 0


def check(cond, what):
    global passed
    if not cond:
        sys.exit(f"FAIL: {what}")
    passed += 1
    print("ok  ", what)


def song(folder, txt_name, headers, files, meta=BY, encoding="utf-8", duet=False):
    d = os.path.join(SONGS, folder)
    os.makedirs(d, exist_ok=True)
    body = "P1\n: 0 4 60 la\nP2\n: 8 4 62 la\nE\n" if duet else ": 0 4 60 la\n: 8 4 62 la\nE\n"
    text = "".join(f"#{k}:{v}\n" for k, v in headers.items()) + body
    open(os.path.join(d, txt_name), "wb").write(text.encode(encoding))
    for name in files:
        open(os.path.join(d, name), "wb").write(("dummy media " + folder + name).encode() * 50)
    if meta:
        json.dump(meta, open(os.path.join(d, "feed-meta.json"), "w"))


def key(name):
    p = os.path.join(W, name + ".pem")
    subprocess.run(["openssl", "genpkey", "-algorithm", "ed25519", "-out", p], check=True, capture_output=True)
    return p


def build(**kw):
    gen.warnings.clear()
    return gen.generate(SONGS, OUT, "test", "Test", **kw)


def lib(name):
    return os.path.join(W, name)


# ---------- fixture ----------
H = {"ARTIST": "Künstler", "TITLE": "Lied", "BPM": "300", "GAP": "1000", "MP3": "Künstler - Lied.mp3",
     "COVER": "Künstler - Lied [CO].jpg", "VIDEO": "Künstler - Lied.mp4"}
song("Künstler - Lied", "Künstler - Lied.txt", H, ["Künstler - Lied.mp3", "Künstler - Lied [CO].jpg", "Künstler - Lied.mp4"])
song("Künstler - Lied", "Künstler - Lied [DUET].txt", H | {"TITLE": "Lied [DUET]"}, [], duet=True)
song("Legacy", "legacy.txt", {"ARTIST": "Café Band", "TITLE": "Été", "BPM": "200", "GAP": "0", "MP3": "Café.mp3",
                              "COVER": "Cover.JPG", "BACKGROUND": "missing.png"}, ["Café.mp3", "cover.jpg"], encoding="cp1252")
song("Bom", "bom.txt", {"ARTIST": "Bom", "TITLE": "Bom", "BPM": "200", "GAP": "0", "MP3": "bom.mp3"}, ["bom.mp3"], encoding="utf-8-sig")
song("Undeclared", "u.txt", {"ARTIST": "Nobody", "TITLE": "Unknown", "BPM": "200", "GAP": "0", "MP3": "u.mp3"}, ["u.mp3"], meta=None)
song("Bad", "bad.txt", {"ARTIST": "Bad", "TITLE": "Traversal", "BPM": "200", "GAP": "0", "MP3": "../evil.mp3"}, [])
song("Takedown", "t.txt", {"ARTIST": "Take", "TITLE": "Down", "BPM": "200", "GAP": "0", "MP3": "t.mp3"}, ["t.mp3"])
k1, k2, k3 = key("k1"), key("k2"), key("k3")
p1, p2, p3 = (gen.public_key(k) for k in (k1, k2, k3))

# ---------- T1 generate + first sync with a pinned key ----------
r = build(keys=[k1], pubs=[p2], sequence=100)
ids = [e["id"] for e in r["catalog"]["songs"]]
check(len(ids) == 6 and "bad-traversal" not in ids, "generator: 6 entries, unsafe reference '../evil.mp3' skipped")
check(any("case/normalization" in w for w in gen.warnings) and any("missing" in w for w in gen.warnings), "generator: warns on case mismatch and missing file")
check(r["distinct_licenses"] == 2 and all("license" in f for e in r["catalog"]["songs"] for f in e["files"]), "every file carries a license object")
check(r["unique_blobs"] < r["file_refs"], f"shared media deduplicated ({r['file_refs']} refs, {r['unique_blobs']} blobs)")
L = lib("lib1")
log = cli.sync(URL + "#ed25519=" + p1.split(":")[1], L)
check(log["feed"] == "accepted" and log["signed"], "client: signed feed accepted with pinned key")
check(sorted(log["synced"]) == ["bom-bom", "cafe-band-ete", "kunstler-lied", "kunstler-lied-duet", "take-down"], "client: 5 declared songs synced")
check(log["hidden_undeclared"] == ["nobody-unknown"], "client: NOLICENSE song hidden by default")
check(os.path.exists(os.path.join(L, "cafe-band-ete", "Café.mp3")) and os.path.exists(os.path.join(L, "cafe-band-ete", "Cover.JPG")),
      "client: non-ASCII name written; cover stored under the txt's spelling")
check(open(os.path.join(L, "bom-bom", "bom.txt"), "rb").read().startswith(b"\xef\xbb\xbf"), "txt bytes untouched (BOM kept)")
log = cli.sync(URL, L)
check(len(log["unchanged"]) == 5 and not log["synced"], "second sync: nothing downloaded")

# ---------- T2 tampering ----------
good = open(OUT + "/feed.json", "rb").read()
bad = good.replace(b"(prototype)", b"(hijacked!)")
open(OUT + "/feed.json", "wb").write(bad)
log = cli.sync(URL, L)
check("signature file missing" in log["feed"] and len(log["unchanged"]) == 5, "tampered feed.json without signature: ignored, cached feed used")
old_sig = os.path.join(OUT, "sig", hashlib.sha256(good).hexdigest() + ".sig")
shutil.copy(old_sig, os.path.join(OUT, "sig", hashlib.sha256(bad).hexdigest() + ".sig"))
check("signature invalid" in cli.sync(URL, L)["feed"], "tampered feed.json with a replayed signature: ignored")

# ---------- T3 rollback + stale location still serves blobs ----------
build(keys=[k1], pubs=[p2], sequence=50)
os.remove(os.path.join(L, "bom-bom", "bom.mp3"))
st = json.load(open(L + "/.subscription.json")); del st["songs"]["bom-bom"]; json.dump(st, open(L + "/.subscription.json", "w"))
log = cli.sync(URL, L)
check("rollback" in log["feed"] and log["synced"] == ["bom-bom"], "older feed.json ignored, but its location still serves blobs")

# ---------- T4 key rotation ----------
build(keys=[k2], pubs=[p3], sequence=200)
log = cli.sync(URL, L)
check(log["feed"] == "accepted" and json.load(open(L + "/.subscription.json"))["trust"] == [p2, p3], "rotation: feed signed by backup key accepted, trust set replaced")
build(keys=[k1], sequence=300)
log = cli.sync(URL, L)
check("signature invalid" in log["feed"] and json.load(open(L + "/.subscription.json"))["sequence"] == 200, "rotated-out key can no longer sign")

# ---------- T5 withdrawal vs. absence ----------
shutil.rmtree(os.path.join(SONGS, "Takedown")); shutil.rmtree(os.path.join(SONGS, "Bom"))
open(os.path.join(L, "take-down", "t.txt"), "ab").write(b"# my local edit\n")
build(keys=[k2], pubs=[p3], sequence=400, removed=["take-down"])
log = cli.sync(URL, L)
check(log["withdrawn"] == ["take-down"] and not os.path.exists(os.path.join(L, "take-down", "t.mp3")), "withdrawn song: cached media deleted")
check(log["kept_modified"] == ["take-down/t.txt"] and os.path.exists(os.path.join(L, "take-down", "t.txt")), "withdrawn song: locally edited txt kept")
check(log["hidden_absent"] == ["bom-bom"] and os.path.exists(os.path.join(L, "bom-bom", "bom.mp3")), "absent song: hidden, files kept")

# ---------- T6 trust on first use, unsigned feeds ----------
log = cli.sync(URL, lib("lib2"))
check(log["signed"] and json.load(open(lib("lib2") + "/.subscription.json"))["trust"] == [p2, p3], "no key in subscription: keys pinned on first use")
shutil.rmtree(OUT); shutil.rmtree(SONGS)
song("One", "one.txt", {"ARTIST": "One", "TITLE": "One", "BPM": "200", "GAP": "0", "MP3": "one.mp3"}, ["one.mp3"])
build(sequence=1, plain=True)
L3 = lib("lib3")
log = cli.sync(URL, L3)
check(not log["signed"] and log["synced"] == ["one-one"], "unsigned feed with plain .json catalog works")
shutil.rmtree(os.path.join(SONGS, "One"))
song("Two", "two.txt", {"ARTIST": "Two", "TITLE": "Two", "BPM": "200", "GAP": "0", "MP3": "two.mp3"}, ["two.mp3"])
build(sequence=2, removed=["one-one"])
log = cli.sync(URL, L3)
check(log["hidden_absent"] == ["one-one"] and not log["withdrawn"] and os.path.exists(os.path.join(L3, "one-one", "one.mp3")),
      "unsigned feed: 'removed' only hides, never deletes")

# ---------- T7 hostile feed ----------
shutil.rmtree(OUT)
blob = b"x" * 1000
hexd = hashlib.sha256(blob).hexdigest()
os.makedirs(os.path.join(OUT, "blobs", hexd[:2])); open(os.path.join(OUT, "blobs", hexd[:2], hexd), "wb").write(blob)
f = lambda role, name, size=1000: {"role": role, "name": name, "sha256": "sha256:" + hexd, "size": size, "license": {"spdx": "CC0-1.0", "url": "https://creativecommons.org/publicdomain/zero/1.0/"}}
hostile = {"a-traversal": "../../x.mp3", "b-backslash": "..\\x.mp3", "c-device": "CON.mp3", "d-trailing-dot": "a.mp3.",
           "e-hidden": ".feed-song.mp3", "f-exe": "evil.exe", "g-colon": "a:b.mp3", "h-absolute": "/etc/x.mp3", "i-long": "a" * 200 + ".mp3"}
songs = [{"id": i, "artist": "x", "title": i, "files": [f("txt", "s.txt"), f("audio", n)]} for i, n in hostile.items()]
songs.append({"id": "j-case-dup", "artist": "x", "title": "j", "files": [f("txt", "s.txt"), f("audio", "A.mp3"), f("cover", "a.MP3")]})
songs.append({"id": "k-oversize", "artist": "x", "title": "k", "files": [f("txt", "s.txt"), f("audio", "a.mp3", 10)]})
songs.append({"id": "../l-bad-id", "artist": "x", "title": "l", "files": [f("txt", "s.txt")]})
songs.append({"id": "m-fine", "artist": "x", "title": "m", "files": [f("txt", "s.txt"), f("audio", "a.mp3"), {"role": "future-role", "name": "../z"}]})
cat = json.dumps({"spec": cli.SPEC, "id": "h", "songs": songs}).encode()
ch = hashlib.sha256(cat).hexdigest()
os.makedirs(os.path.join(OUT, "catalog")); open(os.path.join(OUT, "catalog", ch + ".json"), "wb").write(cat)
json.dump({"spec": cli.SPEC, "name": "hostile", "operator": {"name": "x", "contact": "mailto:x@example.org"}, "sequence": 1,
           "generated": "2026-11-14T00:00:00Z", "catalogs": [{"id": "h", "path": f"catalog/{ch}.json", "sha256": "sha256:" + ch,
                                                             "size": len(cat), "songs": len(songs), "bytes": 1000}]}, open(OUT + "/feed.json", "w"))
L4 = lib("lib4")
log = cli.sync(URL, L4)
check(sorted(log["skipped_unsafe"]) == sorted(list(hostile) + ["j-case-dup", "../l-bad-id"]), "hostile names and ids: all 11 entries skipped")
check(log["synced"] == ["m-fine"] and any("k-oversize" in x for x in log["failed"]), "oversize blob aborted; unknown role ignored; clean entry synced")
written = sorted(os.path.relpath(os.path.join(d, n), W) for d, _, fs in os.walk(W) for n in fs if "lib4" in d or d == W)
check(all(p.startswith("lib4/") or p.endswith(".pem") for p in written) and not os.path.exists(os.path.join(W, "x.mp3")), "nothing written outside the library folder")
try:
    cli.gunzip(gzip.compress(b"0" * 100000), limit=1000); ok = False
except cli.Reject:
    ok = True
check(ok, "gzip bomb limit enforced")
open(OUT + "/feed.json", "w").write(json.dumps({"spec": "karaoke-feed/0.3", "sequence": 9}))
check("unsupported spec" in cli.sync(URL, L4)["feed"], "unknown 0.x minor version rejected")
print(f"\nALL {passed} CHECKS PASSED")
