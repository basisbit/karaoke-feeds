#!/usr/bin/env python3
"""Checks a feed server in "behind a CDN" mode. This script plays the CDN: all requests come from one address
and carry the secret header plus the real client's address in the CDN's client-IP header.
Usage: check_cdn.py <https base URL> <ca.pem> <secret> <client-IP header> [--limits yes|no]"""
import argparse, http.client, ssl, sys, time, urllib.parse
from concurrent.futures import ThreadPoolExecutor

ap = argparse.ArgumentParser()
ap.add_argument("base"); ap.add_argument("ca"); ap.add_argument("secret"); ap.add_argument("ipheader")
ap.add_argument("--limits", default="yes")
a = ap.parse_args()
U = urllib.parse.urlsplit(a.base)
CTX = ssl.create_default_context(cafile=a.ca)
fails = 0


def res(ok, what, info=""):
    global fails
    fails += not ok
    print(("PASS " if ok else "FAIL ") + what + (f"  [{info}]" if info else ""))


def req(headers, path="/feed.json", method="HEAD"):
    try:
        c = http.client.HTTPSConnection(U.hostname, U.port, context=CTX, timeout=20)
        c.request(method, path, headers=headers)
        st = c.getresponse().status
        c.close()
        return st
    except Exception:
        return 0


auth = {"X-Feed-Origin-Auth": a.secret}
res(req({}) == 403, "no secret header: 403 (nobody can bypass the CDN)", str(req({})))
res(req({"X-Feed-Origin-Auth": "wrong"}) == 403, "wrong secret: 403")
res(req({a.ipheader: "198.51.100.1"}) == 403, "forged client address without the secret: 403")
res(req(auth | {a.ipheader: "198.51.100.1"}) == 200, "with the secret: 200")

with ThreadPoolExecutor(16) as ex:  # 400 requests from 400 different players, all through the one "CDN" address
    codes = list(ex.map(lambda i: req(auth | {a.ipheader: f"10.{i // 250}.{i % 250}.1"}), range(400)))
res(codes.count(200) == 400, "400 requests from 400 players through one CDN server: none limited", f"{codes.count(200)} x 200")

with ThreadPoolExecutor(40) as ex:  # 40 slow parallel downloads of different players: no per-connection limit on the CDN
    socks = []
    def start(i):
        import socket
        s = CTX.wrap_socket(socket.create_connection((U.hostname, U.port), timeout=20), server_hostname=U.hostname)
        s.sendall(f"GET /feed.json HTTP/1.1\r\nHost: {U.hostname}\r\nX-Feed-Origin-Auth: {a.secret}\r\n{a.ipheader}: 10.9.{i}.1\r\n\r\n".encode())
        socks.append(s)
        return s.recv(12)[9:12].decode()
    try:
        codes = list(ex.map(start, range(40)))
    except Exception as e:
        codes = [str(e)]
    for s in socks:
        s.close()
res(codes.count("200") == 40, "40 parallel connections from one CDN server: all accepted", f"{codes.count('200')} ok")

if a.limits == "yes":
    time.sleep(2)
    with ThreadPoolExecutor(16) as ex:  # one player sends too much: limited by its real address
        codes = list(ex.map(lambda i: req(auth | {a.ipheader: "203.0.113.9"}), range(500)))
    res(codes.count(429) >= 150 and codes.count(200) >= 150, "500 quick requests from ONE player through the CDN: limited by the player's address",
        f"{codes.count(200)} ok, {codes.count(429)} x 429")
    res(req(auth | {a.ipheader: "203.0.113.10"}) == 200, "another player through the same CDN server is not affected")
else:
    print("INFO per-player rate limit: not available in this server set-up")
print(f"\n{'ALL CDN CHECKS PASSED' if not fails else f'{fails} CDN CHECK(S) FAILED'}")
sys.exit(1 if fails else 0)
