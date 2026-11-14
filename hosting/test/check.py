#!/usr/bin/env python3
"""Black-box checks of a web server serving a karaoke feed (hosting/*). Standard library only.
Usage: check.py <https base URL> <http base URL> <published feed folder> <ca.pem> [--rate yes|no] [--conn yes|no]
Prints PASS/FAIL/INFO lines; exit code 1 if anything failed. Destructive for nothing: it only reads."""
import argparse, gzip, hashlib, json, os, socket, ssl, sys, time, urllib.parse
from concurrent.futures import ThreadPoolExecutor
import http.client

ap = argparse.ArgumentParser()
ap.add_argument("https"); ap.add_argument("http"); ap.add_argument("feed"); ap.add_argument("ca")
ap.add_argument("--rate", default="yes"); ap.add_argument("--conn", default="yes")
a = ap.parse_args()
U, H = urllib.parse.urlsplit(a.https), urllib.parse.urlsplit(a.http)
CTX = ssl.create_default_context(cafile=a.ca)
fails = 0


def res(ok, what, info=""):
    global fails
    fails += not ok
    print(("PASS " if ok else "FAIL ") + what + (f"  [{info}]" if info else ""))


def req(path, method="GET", headers=None, tls=True, read=True):
    c = (http.client.HTTPSConnection(U.hostname, U.port, context=CTX, timeout=20) if tls
         else http.client.HTTPConnection(H.hostname, H.port, timeout=20))
    c.putrequest(method, path, skip_accept_encoding=True)
    for k, v in (headers or {}).items():
        c.putheader(k, v)
    c.endheaders()
    r = c.getresponse()
    body = r.read() if read else b""
    c.close()
    return r.status, {k.lower(): v for k, v in r.getheaders()}, body


def raw(data, timeout=40):
    """Send raw bytes over TLS, return (first response bytes, seconds until the server answered or closed)."""
    s = CTX.wrap_socket(socket.create_connection((U.hostname, U.port), timeout=timeout), server_hostname=U.hostname)
    t = time.time()
    s.sendall(data)
    try:
        got = s.recv(200)
    except (socket.timeout, ConnectionError, ssl.SSLError):
        got = b""
    s.close()
    return got, time.time() - t


feed_raw = open(os.path.join(a.feed, "feed.json"), "rb").read()
feed = json.loads(feed_raw)
cat = feed["catalogs"][0]
songs = json.loads(gzip.decompress(open(os.path.join(a.feed, cat["path"]), "rb").read()))["songs"]
files = {f["sha256"][7:]: f["size"] for s in songs for f in s["files"]}
big = max(files, key=files.get)
blob = f"/blobs/{big[:2]}/{big}"
sig = f"/sig/{hashlib.sha256(feed_raw).hexdigest()}.sig"

# --- the feed's own files ---
st, h, body = req("/feed.json", headers={"Accept-Encoding": "gzip"})
res(st == 200 and body == feed_raw and "content-encoding" not in h, "feed.json: 200, byte-identical")
res(h.get("cache-control") == "public, max-age=300, stale-if-error=86400", "feed.json: Cache-Control max-age=300, stale-if-error", h.get("cache-control"))
res(h.get("content-type", "").startswith("application/json"), "feed.json: Content-Type application/json", h.get("content-type"))
etag = h.get("etag")
st, _, _ = req("/feed.json", headers={"If-None-Match": etag} if etag else {"If-Modified-Since": h.get("last-modified", "")})
res(st == 304, "feed.json: conditional GET gives 304", str(st))
st, h, body = req("/" + cat["path"], headers={"Accept-Encoding": "gzip"})
res(st == 200 and hashlib.sha256(body).hexdigest() == cat["sha256"][7:] and "content-encoding" not in h,
    "catalog .json.gz: 200, hash OK, no Content-Encoding", h.get("content-encoding", ""))
st, h, body = req(sig)
res(st == 200 and len(body) < 200, "signature file: 200")
st, h, body = req(blob, headers={"Accept-Encoding": "gzip, br"})
res(st == 200 and hashlib.sha256(body).hexdigest() == big and "content-encoding" not in h, f"largest blob ({files[big] // 1_000_000} MB): 200, hash OK, no Content-Encoding")
res(h.get("cache-control") == "public, max-age=31536000, immutable", "blob: Cache-Control immutable", h.get("cache-control"))
res(h.get("content-type") == "application/octet-stream", "blob: Content-Type application/octet-stream", h.get("content-type"))
st, h, _ = req(blob, "HEAD")
res(st == 200 and h.get("content-length") == str(files[big]), "HEAD blob: 200 with correct Content-Length")
st, h, body = req(blob, headers={"Range": "bytes=100-199"})
res(st == 206 and len(body) == 100, "range request: 206 with 100 bytes", str(st))

# --- headers on every response ---
for path, label in (("/feed.json", "200"), ("/nothing-here", "404")):
    st, h, _ = req(path)
    want = {"access-control-allow-origin": "*", "x-content-type-options": "nosniff", "cross-origin-resource-policy": "cross-origin",
            "referrer-policy": "no-referrer", "strict-transport-security": "max-age=31536000"}
    missing = [k for k, v in want.items() if h.get(k) != v] + ([] if "default-src 'none'" in h.get("content-security-policy", "") else ["csp"])
    res(not missing, f"security headers on a {label} response", ",".join(missing))
    server = h.get("server", "")
    res(not any(ch.isdigit() for ch in server), f"no version in Server header ({label})", server or "no Server header")
st, h, _ = req("/blobs/00/" + "0" * 64)
res(st == 404 and "immutable" not in h.get("cache-control", ""), "missing blob: 404 and NOT cached for a year", h.get("cache-control", ""))

# --- only what is needed ---
# the landing page: served, with a policy that allows only its own stylesheet and script
pages = [("/", "text/html"), ("/index.css", "text/css"), ("/index.js", "text/javascript")]
pages += [("/songs-" + c["id"] + ".html", "text/html") for c in feed["catalogs"]]
for path, ctype in pages:
    st, h, body = req(path)
    csp = h.get("content-security-policy", "")
    res(st == 200 and h.get("content-type", "").startswith(ctype) and len(body) > 100, f"landing page {path}: 200, {ctype}", f"{st} {h.get('content-type')}")
    res("default-src 'none'" in csp and "script-src 'self'" in csp and "style-src 'self'" in csp and "unsafe" not in csp,
        f"landing page {path}: policy allows only its own files", csp)
    res(h.get("cache-control") == "public, max-age=300", f"landing page {path}: Cache-Control max-age=300", h.get("cache-control"))
st, h, body = req("/songs-" + cat["id"] + ".html", headers={"Accept-Encoding": "gzip"})
res(st == 200 and h.get("content-encoding") == "gzip" and gzip.decompress(body).count(b"<li>") == len(songs),
    "song page: compressed when asked, lists every song", h.get("content-encoding", "not compressed"))
st, _, body = req("/")
res(b"index.css" in body and b"://" not in body.split(b"<body")[0].replace(b"http-equiv", b""), "landing page loads nothing from other sites")

decoys = {"/index.php": "other page", "/blobs/": "folder listing", f"/blobs/{big[:2]}/": "folder listing", "/catalog/": "folder listing",
          "/.secret": "dot file", "/feed.json.tmp": "temp file", "/README.txt": "other file", f"/blobs/{big[:2]}/{big.upper()}": "uppercase hash",
          f"/blobs/{big[:2]}/{big}.mp3": "extension added", "/feed.json/": "trailing slash"}
for path, label in decoys.items():
    st, _, body = req(path)
    res(st == 404, f"{path[:40]} ({label}): 404", str(st))
for path in ("/../../../../etc/passwd", "/blobs/../../../etc/passwd", "/%2e%2e/%2e%2e/%2e%2e/etc/passwd", "/blobs/..%2f..%2f..%2fetc/passwd"):
    st, _, body = req(path)
    res(st in (400, 403, 404) and b"root:" not in body, f"path traversal {path}: refused", str(st))
for m in ("POST", "PUT", "DELETE", "OPTIONS", "TRACE", "PATCH"):
    st, _, _ = req("/feed.json", m)
    res(st == 405, f"{m}: 405", str(st))
for q in ("?x=1", "?", "?nocache=123"):
    st, _, _ = req("/feed.json" + q)
    res(st == 400, f"query string {q!r}: 400", str(st))

# --- http ---
st, h, _ = req("/feed.json", tls=False)
res(st in (301, 308) and h.get("location", "").startswith("https://"), "plain http: permanent redirect to https", f"{st} {h.get('location', '')}")

# --- TLS ---
for ver in (ssl.TLSVersion.TLSv1_2, ssl.TLSVersion.TLSv1_3):
    ctx = ssl.create_default_context(cafile=a.ca)
    ctx.minimum_version = ctx.maximum_version = ver
    try:
        with ctx.wrap_socket(socket.create_connection((U.hostname, U.port), timeout=10), server_hostname=U.hostname) as s:
            res(True, f"{ver.name} accepted", s.cipher()[0])
    except ssl.SSLError as e:
        res(False, f"{ver.name} accepted", str(e))
print("INFO TLS 1.0/1.1 rejection not testable from here: this client's OpenSSL policy refuses them itself")

# --- abuse ---
got, _ = raw(b"GET /feed.json HTTP/1.1\r\nHost: " + U.hostname.encode() + b"\r\nX-Big: " + b"a" * 65536 + b"\r\n\r\n")
res(got[:12] in (b"HTTP/1.1 400", b"HTTP/1.1 431", b"HTTP/1.1 413", b"HTTP/1.0 400") or got == b"", "64 KB request header refused", got[:20].decode(errors="replace"))
got, secs = raw(b"GET /feed.json HTTP/1.1\r\nHost: " + U.hostname.encode() + b"\r\n")  # never finishes the header
res(secs < 30, f"slow client (unfinished header) cut off after {secs:.0f} s", got[:20].decode(errors="replace"))

if a.conn == "yes":
    socks, codes = [], []
    for _ in range(40):  # 40 slow downloads of the largest file at once, from one IP
        try:  # a server may refuse with 429, or by closing the connection before TLS completes
            s = CTX.wrap_socket(socket.create_connection((U.hostname, U.port), timeout=20), server_hostname=U.hostname)
            socks.append(s)
            s.sendall(f"GET {blob} HTTP/1.1\r\nHost: {U.hostname}\r\n\r\n".encode())
            codes.append(s.recv(12)[9:12].decode() or "closed")
        except (socket.timeout, ConnectionError, ssl.SSLError):
            codes.append("closed")
    for s in socks:
        s.close()
    ok, refused = codes.count("200"), len(codes) - codes.count("200")
    res(30 <= ok <= 32 and refused >= 8, "40 parallel downloads from one IP: about 32 allowed, rest refused", f"{ok} ok, others: {sorted(set(codes) - {'200'})}")
    time.sleep(2)
else:
    print("INFO per-IP connection limit: not available in this server set-up")

def burst(_):
    try:
        return req("/feed.json", "HEAD")[0]
    except Exception:
        return 0
if a.rate == "yes":
    with ThreadPoolExecutor(16) as ex:
        codes = list(ex.map(burst, range(500)))
    ok, limited = codes.count(200), codes.count(429)
    res(150 <= ok <= 320 and limited >= 150, "500 quick requests from one IP: about 200 + 20/s allowed, rest 429", f"{ok} ok, {limited} x 429, other {len(codes) - ok - limited}")
    time.sleep(11)
    res(req("/feed.json", "HEAD")[0] == 200, "after 11 s the client is served again")
else:
    with ThreadPoolExecutor(16) as ex:
        codes = list(ex.map(burst, range(500)))
    print(f"INFO per-IP rate limit: not available in this server set-up (500 quick requests: {codes.count(200)} x 200)")

print(f"\n{'ALL CHECKS PASSED' if not fails else f'{fails} CHECK(S) FAILED'}")
sys.exit(1 if fails else 0)
