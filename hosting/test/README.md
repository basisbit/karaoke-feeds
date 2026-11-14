# Tests

There are two test suites: the Go tests of `kfeed`, and the web server suite in this folder. This file records what
they cover, what was measured, and what is not covered.

## kfeed (`tool/`)

```
cd tool && go test ./...
```

Runs in CI on Linux, Windows and macOS (`.github/workflows/ci.yml`), plus the race detector on Linux.

| Test | Covers |
|---|---|
| `TestOperatorWorkflow` | init, keygen, build (add, change, delist, withdraw, rename hints, `previous_ids`, mass-removal guard, repair of missing blobs, purge list), verify (tampering, damaged blobs, signatures, key rotation), serve (headers, ranges), CDN cache detection, the 512 MB warning |
| `TestOutdatedFilesRemoved` | the song list and signature of the previous build stay for one more build and are removed by the build after it, also when nothing changed |
| `TestNames`, `TestSlugAndLicenseURL` | file name rules (SPEC 5.2), ids, Creative Commons license URLs |
| `TestSync` | the player side: catalog choice, updates fetch only changed files, local edits kept / `--replace-local`, hide and show again, withdrawal, renames, older and tampered `feed.json`, unsubscribing, license filter `--exclude` |
| `TestSyncHTTP` | sync over HTTP with 429 back-off, unsigned feeds only from their address, mirror fallback |
| `TestIncompleteDownloads` | downloads that end early without an error: two repeats recover them, `verify --full` warns; a server that always cuts off fails cleanly |
| `TestSyncHostileFeed` | path traversal, device names, wrong extensions, invalid ids, oversized files, unknown roles |
| `TestKeyHandling` | private key from standard input, refusal of key files other users can read, public keys as text or file, a private key given where a public key belongs |
| `TestPrivateFeed` | private feeds: the settings check, the page, sync through a personal address with a redirect for the big files, no `Referer` to the redirect target, the address neither in messages nor readable in the state folder, access ended, wrong token, a replaced token, the address from standard input |
| `TestServeOnlyFeedFiles` | `kfeed serve` hands out only the feed's files: no hidden, temporary or other files, nothing outside the folder (also with `\` and drive names on Windows), and the same answer for existing and missing files outside it |
| `TestFileURL` | `file://` addresses, including `C:` paths on Windows |
| `TestLandingPage` | the web page: escaping, no inline code, nothing loaded from other sites, song pages, headers from `kfeed serve` |
| `TestLandingPageLargeFeed`, `TestThousands` | 30,000 songs: the main page stays small (about 5 KB), every song is on its category page, pages of removed categories disappear; number formatting |

## Web servers (`hosting/test/`)

`run.sh` runs in a Fedora container (distrobox, see the comment at its top). For each of nginx, Apache and Caddy it:

1. builds a signed test feed from the Creative Commons songs of `UltraStar-Deluxe/songs` and serves it with the
   configuration from `hosting/`, with the open-file limits a systemd service gets by default;
2. checks it with `kfeed verify --full`;
3. runs `check.py`: headers, caching, ranges, the web page and song pages with their policy and compression, 404s for everything else, path traversal,
   methods, query strings, the http redirect, TLS versions, oversized headers, slow clients, per-IP connection and
   request limits;
4. runs `load.py`: 1,024 downloads at once (see below), and checks the open-file limits and the error log;
5. checks logging: no access log, the error log in its own file, 16 forced rotations leave 14 old files;
6. restarts the server in CDN mode and runs `check_cdn.py`: secret header, per-player limits through one CDN server;
7. restarts the server in private mode and runs `check_private.py`: nothing without a listed token, the same files
   and headers with one (but `Cache-Control: private`), the usual restrictions behind the token, `kfeed verify`
   through a personal address, a token taken off the list; then once more with the big files sent on to another
   address (the redirect, and no redirect without a valid token).

`PRIVATE_ONLY=1` runs only step 7.

Last full run: 2026-11-14, nginx 1.30.6, Apache 2.4.71 with mod_qos 11.76, Caddy 2.11.4, logrotate 3.22, Fedora 45.
All checks passed. Caddy has no rate limits without a plugin, so those checks report "not available" for it.

Variables for comparisons: `NGINX_MAIN=/dev/null`, `APACHE_TUNING=/dev/null`, `APACHE_NOFILE=1024` (run without the
corresponding file), `LOAD_ARGS="--ips 300 --rate 256K --only http1.1"`.

### Many downloads at once

Setup: Fedora 45, a simulated small server: nginx with 2 workers, Caddy with 2 CPU threads; Apache uses its process settings. 1,024 downloads at once from 128 client addresses, each limited to 512 KB/s like a home connection. Run twice: over HTTP/1.1, one connection per download (1,024 connections), and over HTTP/2, 8 downloads per connection.

| | All 1,024 complete | Fairness¹ HTTP/1.1 / HTTP/2 | Longest wait for the first byte, HTTP/1.1 / HTTP/2 |
|---|---|---|---|
| nginx with these settings | yes | 0.990 / 0.997 | 3.2 s / 2.0 s |
| Caddy | yes | 0.989 / 0.994 | 3.7 s / 2.8 s |
| Apache with these settings | yes | 0.990 / 1.000 | 3.5 s / 0.5 s |
| Apache **without** `tuning.conf` | yes, but "server reached MaxRequestWorkers" | 0.954 / 0.999 | **9.1 s** / 1.3 s |
| Apache **without** `limits.conf` | in one of two runs "Too many open files": 16 failed HTTP/2 connection setups | — | — |
| nginx with these settings, 2,400 connections² | yes | 0.995 | 4.3 s |
| nginx **without** `main.conf`, 2,400 connections² | **no**: 84 × "accept4() failed (24: Too many open files)", 2 downloads cut off | 0.932 | **26.2 s** |

¹ Jain's fairness index over the speed of all downloads: 1.0 means every download got exactly the same speed. Waits for the first byte above 1 s are mostly TLS handshakes; the test machine ran clients and server at once.
² HTTP/1.1 only, 300 client addresses × 8, each limited to 256 KB/s. In the run with the settings, one download crawled at 0.02 MB/s although all others ran at full speed; the cause is not known (possibly the test client).

## Also tested by hand

- 2026-11-14: the 46 Creative Commons songs written by `kfeed sync` into the songs folder of UltraStar Deluxe
  2026.11.0 (Flathub) on Linux. Everything played: video, umlauts and a UTF-8 BOM, a missing background, two charts
  in one folder, a colon in the title, a song without `#LANGUAGE`. USDX logs "AnalyseFile failed" for every
  `license.txt`; harmless.
- 2026-11-14: the web page in Firefox 160 (headless), light and dark mode, desktop and phone width, served by
  `kfeed serve` with its security policy: the example feed (46 songs) and a generated feed with 30,000 songs in three
  categories (`kfeed build` 4.5 s; main page 6 KB; song pages 1.1 MB, 84 KB compressed).
- The Go tests as a Windows build under Wine.

## Not covered yet

- Refusing TLS 1.0/1.1 (the test machine's own OpenSSL can't offer them), IPv6 listeners, the Let's Encrypt
  challenge path, Debian's file layout (`sites-available`, `apache2`).
- The Caddy rate-limit plugin.
- Disk speed (the test files fit into memory), a real network link (the tests run over loopback), kernel TLS.
- The sysctl file and the systemd drop-in themselves (the test applies the same limits directly).
- The CDN set-ups: Cloudflare R2 and the `rclone` upload, Cloudflare and bunny.net dashboard settings, firewall
  allowlists. Whether an R2 custom domain serves `index.html` for `/`.
- `kfeed` on real Windows and macOS machines outside CI, and on macOS at all before the first CI run.
- Games other than UltraStar Deluxe; duets.
