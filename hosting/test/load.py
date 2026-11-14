#!/usr/bin/env python3
"""Concurrent-download load test: many client IPs (127.0.x.y) x parallel downloads, each throttled like a home
connection, so that all downloads are open at the same time. Uses curl.
Usage: load.py <https base URL> <published feed folder> <ca.pem> [--ips 128] [--per-ip 8] [--rate 512K]
Prints PASS/FAIL/INFO; exit code 1 on failure."""
import argparse, gzip, json, os, random, statistics, subprocess, sys, time
from concurrent.futures import ThreadPoolExecutor

ap = argparse.ArgumentParser()
ap.add_argument("base"); ap.add_argument("feed"); ap.add_argument("ca")
ap.add_argument("--ips", type=int, default=128); ap.add_argument("--per-ip", type=int, default=8)
ap.add_argument("--rate", default="512K")
ap.add_argument("--only", choices=["http1.1", "http2"], help="run only one of the two phases")
a = ap.parse_args()
feed = json.load(open(os.path.join(a.feed, "feed.json")))
cat = feed["catalogs"][0]
songs = json.loads(gzip.decompress(open(os.path.join(a.feed, cat["path"]), "rb").read()))["songs"]
files = {f["sha256"][7:]: f["size"] for s in songs for f in s["files"] if f["role"] == "audio" and 2e6 <= f["size"] <= 12e6}
random.seed(1)
pool = sorted(files)


def client(i, proto):
    ip = f"127.0.{1 + i // 200}.{10 + i % 200}"
    picks = [random.choice(pool) for _ in range(a.per_ip)]
    cmd = ["curl", "-s", proto, "--interface", ip, "--cacert", a.ca, "--parallel", "--parallel-max", str(a.per_ip), "--parallel-immediate",
           "--limit-rate", a.rate, "--max-time", "300",
           "-w", "%{http_code} %{size_download} %{speed_download} %{time_starttransfer} %{time_total} %{url_effective}\\n"]
    for h in picks:
        cmd += ["-o", "/dev/null", f"{a.base}/blobs/{h[:2]}/{h}"]
    out = subprocess.run(cmd, capture_output=True, text=True, timeout=600).stdout
    return [l.split() for l in out.splitlines() if l.strip()]


failed = False
n = a.ips * a.per_ip
for proto, label in (("--http1.1", f"HTTP/1.1, one connection per download: {n} connections"),
                     ("--http2", f"HTTP/2, 8 downloads per connection: {a.ips} connections")):
    if a.only and proto != "--" + a.only:
        continue
    t0 = time.time()
    with ThreadPoolExecutor(a.ips) as ex:
        rows = [r for res in ex.map(lambda i: client(i, proto), range(a.ips)) for r in res]
    wall = time.time() - t0
    ok = [r for r in rows if r[0] == "200" and int(r[1]) == files[r[5].rsplit("/", 1)[1]]]
    bad = [f"{r[0]} {r[1]}" for r in rows if r not in ok]
    failed |= len(ok) != n
    print(("PASS" if len(ok) == n else "FAIL") + f" {n} downloads at once, each limited to {a.rate}B/s ({label}): {len(ok)} complete with the right size"
          + (f"  [problems: {sorted(set(bad))[:5]}, missing {n - len(rows)}]" if len(ok) != n else ""))
    if ok:
        speeds = [float(r[2]) / 1e6 for r in ok]
        ttfb = sorted(float(r[3]) for r in ok)
        jain = sum(speeds) ** 2 / (len(speeds) * sum(x * x for x in speeds))
        print(f"INFO   {sum(int(r[1]) for r in ok) / 1e9:.2f} GB in {wall:.1f} s; speed per download MB/s min {min(speeds):.2f}, median {statistics.median(speeds):.2f}, max {max(speeds):.2f};"
              f" fairness (Jain, 1.0 = all equal) {jain:.3f}")
        print(f"INFO   time to first byte s: median {statistics.median(ttfb):.2f}, p99 {ttfb[int(len(ttfb) * .99) - 1]:.2f}, max {ttfb[-1]:.2f}")
    time.sleep(2)
sys.exit(1 if failed else 0)
