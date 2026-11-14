#!/usr/bin/env python3
"""Checks a feed server in "private feed" mode: only personal addresses /s/<token>/... with a listed token work.
Usage: check_private.py <https base URL> <ca.pem> <feed folder> <valid token> [--blobs <address>]
--blobs: the server is set up to send downloads of the big files on to this address (a CDN or a file store)."""
import argparse, glob, hashlib, http.client, json, os, ssl, sys, urllib.parse

ap = argparse.ArgumentParser()
ap.add_argument("base"); ap.add_argument("ca"); ap.add_argument("feed"); ap.add_argument("token")
ap.add_argument("--blobs", default="")
a = ap.parse_args()
U = urllib.parse.urlsplit(a.base)
CTX = ssl.create_default_context(cafile=a.ca)
fails = 0


def res(ok, what, info=""):
    global fails
    fails += not ok
    print(("PASS " if ok else "FAIL ") + what + (f"  [{info}]" if info else ""))


def get(path, headers=None, method="GET"):
    c = http.client.HTTPSConnection(U.hostname, U.port, context=CTX, timeout=20)
    c.request(method, path, headers=headers or {})
    r = c.getresponse()
    body = r.read()
    c.close()
    return r.status, {k.lower(): v for k, v in r.getheaders()}, body


def disk(rel):
    with open(os.path.join(a.feed, rel), "rb") as f:
        return f.read()


feed = disk("feed.json")
catalog = json.loads(feed)["catalogs"][0]["path"]
sig = "sig/" + hashlib.sha256(feed).hexdigest() + ".sig"
blob = os.path.relpath(sorted(glob.glob(os.path.join(a.feed, "blobs", "*", "*")))[0], a.feed).replace(os.sep, "/")
T = "/s/" + a.token
WRONG = "/s/" + "x" * len(a.token)

# --- without a valid token nothing is served
for path in ("/feed.json", "/", "/index.html", "/" + catalog, "/" + blob):
    st, _, _ = get(path)
    res(st == 403, f"no token: {path[:40]} is refused", str(st))
for path in ("/feed.json", "/", "/" + blob):
    st, _, _ = get(WRONG + path)
    res(st == 403, f"unknown token: {path[:40]} is refused", str(st))
st, _, _ = get("/s/short/feed.json")
res(st in (403, 404), "malformed token is refused", str(st))
st, _, _ = get("/s//feed.json")
res(st in (403, 404), "empty token is refused", str(st))
st, _, _ = get(T)
res(st in (403, 404), "the token alone, without a path, is no address", str(st))

# --- with the token: the same files and headers as a public feed, but not for shared caches
st, h, body = get(T + "/feed.json", {"Accept-Encoding": "gzip"})
res(st == 200 and body == feed, "valid token: feed.json arrives byte for byte", str(st))
res(h.get("content-type", "").startswith("application/json") and "content-encoding" not in h, "feed.json: JSON, not compressed",
    f"{h.get('content-type')} {h.get('content-encoding')}")
cc = h.get("cache-control", "")
res(cc.startswith("private") and "max-age=300" in cc and "public" not in cc, "feed.json: Cache-Control private, 5 minutes", cc)
for rel in (catalog, sig):
    st, h, body = get(T + "/" + rel)
    cc = h.get("cache-control", "")
    res(st == 200 and body == disk(rel) and cc.startswith("private") and "immutable" in cc and "public" not in cc,
        f"valid token: {rel.split('/')[0]}/ byte for byte, Cache-Control private + immutable", f"{st} {cc}")
st, h, body = get(T + "/")
csp = h.get("content-security-policy", "")
res(st == 200 and body == disk("index.html"), "valid token: the web page is served", str(st))
res("style-src 'self'" in csp and "script-src 'self'" in csp, "web page: its own stylesheet and script are allowed", csp[:60])
res(h.get("referrer-policy") == "no-referrer", "web page: links don't tell other sites the personal address", str(h.get("referrer-policy")))
st, h, _ = get(T + "/index.css")
res(st == 200 and h.get("content-type", "").startswith("text/css"), "valid token: the page's stylesheet is served", str(st))
st, h, _ = get(T + "/" + catalog)
res("style-src" not in h.get("content-security-policy", "x style-src"), "feed files: may load nothing at all", h.get("content-security-policy", "")[:50])

if a.blobs:
    st, h, _ = get(T + "/" + blob)
    res(st == 302 and h.get("location") == a.blobs + "/" + blob, "valid token: big files are sent on to the other address",
        f"{st} {h.get('location')}")
    res("cache-control" not in h, "the redirect itself is not cacheable", h.get("cache-control", ""))
    st, h, _ = get(WRONG + "/" + blob)
    res(st == 403 and "location" not in h, "unknown token: no redirect", str(st))
    st, _, body = get(T + "/" + catalog)
    res(st == 200 and body == disk(catalog), "catalogs still come from this server", str(st))
else:
    st, h, body = get(T + "/" + blob)
    res(st == 200 and body == disk(blob) and h.get("content-type") == "application/octet-stream" and "content-encoding" not in h,
        "valid token: a big file arrives byte for byte", f"{st} {h.get('content-type')}")
    st, h, body = get(T + "/" + blob, {"Range": "bytes=0-9"})
    res(st == 206 and body == disk(blob)[:10], "valid token: range requests work", str(st))

# --- the usual restrictions still hold behind the token
for path, want in (("/.secret", 404), ("/README.txt", 404), ("/blobs/", 404), ("/feed.json?x=1", 400), (T + "/feed.json", 404)):
    st, _, _ = get(T + path)
    res(st == want, f"valid token: {path[:30]} is {want}", str(st))
st, _, _ = get(T + "/feed.json", method="POST")
res(st == 405, "valid token: POST is 405", str(st))

print(f"\n{'ALL PRIVATE FEED CHECKS PASSED' if not fails else f'{fails} PRIVATE FEED CHECK(S) FAILED'}")
sys.exit(1 if fails else 0)
