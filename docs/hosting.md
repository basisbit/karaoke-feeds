# Hosting a karaoke feed: guide for server admins

This guide is for whoever runs the web server. You don't need to know anything about karaoke or about the feed's content.
Related guides: [operator-guide.md](operator-guide.md) (how the folder is produced), [signing.md](signing.md).

## 1. What you are hosting

A folder of static files. No database, no scripts, no accounts, no uploads from users. The feed operator produces the folder with `kfeed build` and hands it to you (or uploads it).

```
public/
  feed.json            small, changes with every update
  sig/<hash>.sig       tiny signature files (signed feeds only)
  catalog/<hash>.json.gz   song lists, a few KB to a few MB
  blobs/<xx>/<hash>    the songs: text, audio, video, images. This is where the gigabytes are.
```

Everything except `feed.json` is **immutable**: a file name is the checksum of its content, so a given URL always returns the same bytes. Games verify every file against its checksum.

## 2. What the server must do

| Requirement | Why |
|---|---|
| Serve the folder at a stable URL over **HTTPS** | `https://feed.example.org/feed.json` is what players subscribe to. Moving it later loses subscribers. |
| Deliver files **byte for byte** | No minifying, no image optimisation, no rewriting. A changed byte fails the checksum and the game discards the file. |
| No `Content-Encoding` label on stored files | In particular, don't announce `.json.gz` files as `Content-Encoding: gzip`. Normal on-the-fly compression is fine. |
| Support **range requests** | Games play song previews by fetching a part of the audio file. All common web servers do this for static files. |
| Header `Access-Control-Allow-Origin: *` on all files | Needed by games that run in a browser. Harmless for public static files. |
| `Cache-Control: public, max-age=300, stale-if-error=86400` on `feed.json` | Updates and takedowns reach players within minutes; a CDN may keep serving it for a day if your server fails. |
| `Cache-Control: public, max-age=31536000, immutable` on `blobs/`, `catalog/`, `sig/` | Lets CDNs and games keep these files without asking again. |
| No directory listings (recommended) | Nothing breaks with them, they're just not needed. |

You don't need cookies, logins, URL parameters or redirects. Games send none and expect none. (A feed for members only works with personal addresses instead of logins, see section 9.)

After any change to the server, run the check in §6. It tests all of the above from outside.

## 3. Uploading an update

Order matters, because games may fetch while you upload:

1. everything **except** `feed.json` (new blobs, catalogs, signatures)
2. `feed.json`
3. delete files that are no longer in the local folder

With rsync over SSH:

```sh
#!/bin/sh
set -e
SRC=my-feed/public/
DST=user@server:/var/www/feed/

rsync -a --exclude=/feed.json "$SRC" "$DST"          # 1. new files first
rsync -a "$SRC"feed.json "$DST"                       # 2. then the entry point
rsync -a --delete --delete-after "$SRC" "$DST"        # 3. then clean up
```

`kfeed build` keeps the previous song list and signature in the folder for one more build, so games that are in the middle of an update still find what they need. A game that asks for a file that was just deleted gets a 404 and simply retries later; that is expected and harmless.

If the build machine and the web server are the same machine, don't serve the build output folder directly from a path the operator is still working in; copy it with the script above (the destination can be a local path).

## 4. Web server configuration

Ready-made configurations are in the `hosting/` folder of this repository:

| Server | Site file | Server-wide files (4.3) | Per-client rate limits |
|---|---|---|---|
| nginx | [hosting/nginx/feed.conf](../hosting/nginx/feed.conf) | [main.conf](../hosting/nginx/main.conf): merge into `nginx.conf` | built in |
| Apache httpd 2.4 | [hosting/apache/feed.conf](../hosting/apache/feed.conf) | [tuning.conf](../hosting/apache/tuning.conf), systemd [limits.conf](../hosting/apache/limits.conf) | with `mod_qos` (Fedora/RHEL package `mod_qos`) |
| Caddy 2 | [hosting/caddy/Caddyfile](../hosting/caddy/Caddyfile) | none needed | **none built in**: use a CDN, or a Caddy build with a plugin (see the file) |
| all (optional) | | [hosting/sysctl/99-karaoke-feed.conf](../hosting/sysctl/99-karaoke-feed.conf): Linux network settings | |

To use one:
1. Copy the file to the place named at its top. For nginx and Apache, also copy the `logrotate` file next to it to `/etc/logrotate.d/karaoke-feed` and create the log folder (4.2), and install the server-wide files from the table (4.3).
2. Replace the placeholders listed at its top: host name, feed folder, certificate paths.
3. Check the syntax (`nginx -t`, `apachectl configtest`, `caddy validate --config Caddyfile`), then reload the server.
4. Run `kfeed verify https://your-host/` (§6).

All three do the same:
- **Read-only.** `GET` and `HEAD` only; anything else gets `405`.
- **Only the feed's files.** `feed.json`, `sig/<hash>.sig`, `catalog/<hash>.json(.gz)` and `blobs/<xx>/<hash>` are served, plus the feed's web page (`/`, `index.html`, `index.css`, `index.js`, `songs-<category>.html`; see the operator guide). The page files get their own security policy that allows only their own stylesheet and script, a 5-minute cache time, and compression when the browser asks for it; feed files are never compressed. Everything else is `404`: folder listings, dot files, leftovers like `feed.json.tmp`, and other files that happen to be in the folder. Symlinks are not followed; the feed uses hard links.
- **No query strings.** `/feed.json?x=1` gets `400`. Games never send query strings, and an attacker could use random ones to bypass every cache and hit the origin directly.
- **Headers as in §2**, plus `X-Content-Type-Options: nosniff`, a `Content-Security-Policy` that allows nothing, `Cross-Origin-Resource-Policy: cross-origin`, `Referrer-Policy: no-referrer` and HSTS. They are sent with error responses too. `Cache-Control` is sent only with successful responses, so a `404` is never cached for a year. Blobs are always `application/octet-stream`; the server never guesses a type from content.
- **No compression and no version number** in the `Server` header.
- **TLS 1.2 and 1.3 only** (Mozilla "intermediate" profile), and plain http redirects to https.
- **Slow clients are cut off:** a request header has to arrive within about 10 s, and headers larger than about 8 KB are refused. Downloads themselves have no time limit, because a video over a slow line can take minutes.
- **Per-client limits** (nginx, and Apache with mod_qos): see 4.1.
- **No access log, error log kept 14 days**: see 4.2.

HSTS tells browsers to use only https for this host name for a year. Leave it out if the same host name must also serve plain http.

### 4.1 Security and rate limits

The limits are per client IP address:

| Limit | Value | Why this value |
|---|---|---|
| Requests | 20 per second on average, bursts up to 200 | A game that syncs a category fetches one file after the other. A category of about 50 songs (some 200 files) fits into one burst. |
| Parallel requests or connections | 32 | Games download a few files at once. 32 leaves room for several devices behind one router. |
| Request header size | about 8 KB | Real requests are a few hundred bytes. |
| Time to send the request header | 10 s (Apache: up to 20 s while data keeps arriving) | Stops "slowloris" clients that hold connections open by sending very slowly. |

A client over the limit gets `429 Too Many Requests` and retries later; games are expected to handle this. Each server enforces the limits differently:

- **nginx** works like a bucket: after a burst, the client continues at 20 requests per second.
- **Apache mod_qos** counts 200 requests per 10 seconds. A client over that is refused for the rest of the 10 seconds. A connection over the limit of 32 is closed straight away, without an HTTP status.
- **Caddy** has no limits without a plugin.

**Many players behind one IP address.** A convention venue, a school, a hotel, or a mobile network that puts thousands of customers behind one IP address all count as one client. If players report `429` errors, raise the limits (e.g. `rate=50r/s burst=500` and `limit_conn feed_conn 100` in nginx). For an event, the better answer is a local copy of the feed with `kfeed serve` (§8): it is faster and uses no internet at all.

**Behind a CDN** your server sees the CDN's addresses, not the players'. The per-IP limits would then throttle the CDN itself. Switch the configuration to CDN mode (5.1): the limits then count the real players, and only the CDN can reach your server.

**What rate limits don't do.** They protect against a single misbehaving client, a game stuck in a retry loop, or someone scraping. They don't stop a flood of traffic from thousands of machines (a volumetric DDoS) that fills the server's network connection before any web server setting applies. Against that, only a CDN or your hosting provider's DDoS protection helps. For a public feed, that is one more reason for a CDN.

**What else to keep in mind:**
- Keep the server's operating system and web server updated automatically. The feed has no code of its own; the web server is the only attack surface.
- Upload with a separate account that can write only the feed folder (rsync over SSH with a key). The web server's own user needs read access only.
- Nothing in the feed is secret. Protect write access to the files, not read access.

### 4.2 Logging

The examples write **no access log**: there is no record of who downloaded what, and no IP addresses for normal downloads. Only warnings and errors are logged, for example refused clients and broken requests, and those lines contain IP addresses. They are kept for **14 days**:

| Server | Error log | Rotation |
|---|---|---|
| nginx | `/var/log/nginx/feed/error.log` | [hosting/nginx/logrotate](../hosting/nginx/logrotate) → `/etc/logrotate.d/karaoke-feed`: daily, 14 old files, compressed, older ones deleted |
| Apache | `/var/log/httpd/feed/error.log` (Debian: `/var/log/apache2/feed/`) | [hosting/apache/logrotate](../hosting/apache/logrotate), same rules |
| Caddy | `/var/log/caddy/error.log` | Caddy itself: new file daily, deleted after 14 days. No logrotate needed. |

Set-up, once:
- nginx and Apache: create the folder (`mkdir -m 0750 /var/log/nginx/feed`) and copy the logrotate file. The feed's log has its own folder because the distribution already rotates `/var/log/nginx/*.log` with its own rules, and logrotate refuses a file that two rules cover.
- Caddy: the folder `/var/log/caddy` must be writable by the user Caddy runs as.

Two limits to know:
- **The access log is switched off only for the feed's site.** Other sites on the same server keep their settings, and so does the server's main error log (start-up and configuration errors).
- **Apache has no switch to turn the access log off.** The example sends it to `/dev/null`, which also stops this site from writing into the server's main access log. The test checks that the main access log stays empty.

Without an access log you can't see download numbers. If you want them, a CDN's statistics give totals without storing anything on your server.

### 4.3 Many downloads at once

A feed server spends its time on long downloads from many slow home connections at the same time. The default settings of nginx and Apache are sized for short web pages. The server-wide files from the table above change that:

| Server | What the files change | Why |
|---|---|---|
| nginx | `main.conf`: one worker per CPU core, 4,096 connections per worker, 20,000 open files per worker | Each download needs 2 open files (connection + file). The default limit is 1,024 per process, so a worker would fail at about 500–1,000 downloads. nginx raises the limit itself; no systemd change is needed. |
| nginx | `feed.conf`: file reads in a thread pool (`aio threads`), larger read buffers, a cache of open files, `reuseport`, a longer connection queue | A slow disk can't stall the other downloads; new connections are spread evenly over the workers; files many people download are opened once. |
| Apache | `tuning.conf`: 1,024 worker threads (16 processes × 64) | With HTTPS, every running download occupies one worker thread until it finishes. The default (400 on Fedora) makes everyone above 400 wait. |
| Apache | `limits.conf` (systemd): 65,536 open files per process | HTTP/2 connections need several file descriptors each in Apache; the default of 1,024 per process runs out. |
| Caddy | nothing | One process with lightweight threads; the distribution's service file already allows 1,048,576 open files. |

**Fairness** comes from four things:
- the per-client limits (4.1), so one client can't take more than 32 parallel downloads;
- the server serving all connections in turn;
- for nginx, `reuseport`, which spreads connections evenly over the workers;
- on the network link, the Linux packet scheduler: `fq` in the optional sysctl file shares the uplink per connection.

**What usually runs out first is the network, not the server.** 1 Gbit/s is about 125 MB/s: 250 players downloading at 0.5 MB/s each. With the settings above, the software handles several times that. If your provider's bandwidth or traffic volume is the limit, a CDN (§5) is the answer, not more tuning.

### Hosting without a server of your own

Static-file hosting and object storage (the kind where you upload a folder and get a URL) work, as long as the provider delivers files unchanged and supports range requests. Check the provider's limits on file size and total size before you start: videos are often 20–60 MB each and a feed can reach many gigabytes. Where you can't set headers, the feed still works, but the check in §6 will warn that caching is not optimal. Rate limiting and DDoS protection are then the provider's job; check what they offer.

## 5. A CDN in front (optional, recommended for public feeds)

Because almost everything is immutable, a CDN can serve nearly all traffic: your server delivers each file about once per CDN location, and the CDN absorbs peaks and attacks. Players subscribe to the CDN address, so use a host name you control (`feed.example.org`), not one that belongs to the CDN. Then you can change CDN later without losing subscribers.

| | bunny.net (pull zone) | Cloudflare |
|---|---|---|
| Can it serve a feed from your server? | yes | **not on Free/Pro/Business plans**, see 5.2 |
| Price (checked 2026-11-14) | $0.01/GB in Europe and North America, more elsewhere; $1/month minimum | R2 storage: $0.015/GB-month, **no traffic fees**, 10 GB and 10 million reads/month free |
| Largest file it caches | no limit found in the docs | 512 MB (Enterprise: 5 GB) |
| Client address sent to your server | `X-Real-IP` | `CF-Connecting-IP` |
| Company location (GDPR) | EU (Slovenia) | USA |

Prices and terms change. Check the provider's current pages before you decide.

### 5.1 Your server behind a CDN ("CDN mode")

The configurations in `hosting/` have a CDN mode, switched off by default. Each file says which lines to change. In CDN mode:
- **Only the CDN gets in.** The CDN adds a secret header (`X-Feed-Origin-Auth: <your secret>`) to every request it sends to your server, and the server answers `403` to anything without it. Nobody can bypass the CDN, and nobody can forge a player's address to dodge the limits.
- **The limits count the players.** The server takes the player's address from the CDN's header (`X-Real-IP` or `CF-Connecting-IP`) instead of the CDN server's address. Apache stops limiting connections per address, because all connections come from a few CDN servers.

Caddy has no rate limits, so for Caddy only the secret check applies.

Setting it up:
1. Make a long random secret: `openssl rand -hex 32`. Put it into the configuration as described there, and into the CDN:
   - **bunny.net:** Pull zone → Edge Rules → action "Set Request Header" `X-Feed-Origin-Auth` with the secret, for all requests.
   - **Cloudflare:** Rules → Transform Rules → "Modify Request Header": set `X-Feed-Origin-Auth`. (Only applies to a proxied origin server; R2 doesn't need it.)
2. Check with `curl -I https://your-origin/feed.json`: it must give `403`. Then `kfeed verify https://feed.example.org/` through the CDN.
3. Optional, stronger: also let your firewall accept web traffic only from the CDN's addresses. bunny.net publishes its list at `https://api.bunny.net/system/edgeserverlist/plain` and says it changes, so update it regularly. Cloudflare publishes its ranges on its website.
4. If the secret leaks, change it in both places. Rotate it when people with access leave.

### 5.2 Cloudflare

**Read this first.** Cloudflare's terms for its CDN say that, unless you are an Enterprise customer, you must use specific paid services (named: the Developer Platform, Images and Stream) to serve video and other large files through the CDN. Cloudflare may limit or switch off the CDN for sites that serve video or "a disproportionate percentage of pictures, audio files, or other large files" without them. A karaoke feed is mostly audio and video. **Putting Cloudflare's proxy in front of your own server breaks these terms** on Free, Pro and Business plans.

The way that fits the terms is **R2**, Cloudflare's file storage, which belongs to the Developer Platform. You upload the published folder to an R2 bucket and serve it under your own host name. Downloads from R2 cost nothing, only the storage does. There is no server of your own; sections 3, 4 and 5.1 don't apply.

Setting up R2:
1. Create a bucket and connect your own host name to it ("Custom Domains"). **Don't use the `r2.dev` address** for players: Cloudflare limits it, it doesn't cache, and Cloudflare says it is for testing only. Then turn `r2.dev` off.
2. Bucket CORS policy: allow `GET` and `HEAD` from origin `*`.
3. Upload in the order from section 3, with the headers that a web server would add. Files in R2 keep the `Content-Type` and `Cache-Control` they were uploaded with. With `rclone` (configured for R2 as remote `r2:`):
   ```sh
   SRC=my-feed/public; DST=r2:my-feed-bucket
   IMM="Cache-Control: public, max-age=31536000, immutable"
   for d in blobs catalog sig; do
     rclone copy "$SRC/$d" "$DST/$d" --header-upload "$IMM" --header-upload "Content-Type: application/octet-stream"
   done
   rclone copyto "$SRC/feed.json" "$DST/feed.json" \
     --header-upload "Cache-Control: public, max-age=300, stale-if-error=86400" --header-upload "Content-Type: application/json"
   rclone sync "$SRC" "$DST"   # last: deletes what was removed; uploads nothing new
   ```
4. Add a **Cache Rule** for the host name: "Eligible for cache", Edge TTL "use cache-control header if present". This is needed whether you use R2 or Enterprise: Cloudflare decides what to cache by file extension. Blobs have none and `.json` isn't on its list, so without the rule nothing but some catalogs would be cached. `kfeed verify` warns when Cloudflare doesn't cache.
5. Add a WAF custom rule that blocks requests with a query string (`http.request.uri.query ne ""`) and with methods other than `GET`/`HEAD`. Your own server would answer these with `400`/`405`. R2 can't, and each such request costs a read.
6. Turn on Tiered Cache, so that one Cloudflare location fetches from R2 and the others fetch from it.
7. Files over 512 MB are not cached on Free, Pro and Business; `kfeed build` warns about them.

Cloudflare caches `404` answers for 3 minutes if the response has no `Cache-Control`. That's harmless with the upload order from section 3.

The web page: upload `index.html`, `index.css`, `index.js` and the `songs-*.html` files with `Content-Type` `text/html`, `text/css` and `text/javascript` and `Cache-Control: public, max-age=300`. If your R2 custom domain doesn't show `index.html` for the address `/`, add a Cloudflare rule that rewrites `/` to `/index.html`, or link to `/index.html`.

### 5.3 bunny.net

A pull zone fetches from your server. Configure your server as in section 4 with CDN mode (5.1). Settings to check in the pull zone (names as in bunny.net's documentation; menus may differ):
- **Caching:** let the zone respect your server's `Cache-Control`. Turn on serving stale content while the origin is offline (it pairs with `stale-if-error` on `feed.json`), Origin Shield and request coalescing. These mean your server gets one request per file instead of one per location.
- **Vary cache:** leave the query string out of the cache key, and add an Edge Rule that blocks requests with a query string. Your server answers them with `400` anyway, but blocking at the edge keeps them off your server.
- **Leave off** everything that changes files: the image optimizer and similar features. They would break checksums.
- **Security:** add per-IP limits in the zone or in Bunny Shield. They protect the CDN bill; CDN mode protects your server.
- Perma-Cache (copies files into Bunny storage) fits immutable files well. On a takedown, purge it as well as the normal cache.

### 5.4 Publishing and takedowns with a CDN

- **Normal updates:** upload as in section 3. Players see the new `feed.json` within 5 minutes, when the CDN's copy expires. To make it immediate, purge `feed.json` in the CDN after uploading.
- **Takedowns:** purge `feed.json` **and** every URL from the list `kfeed build` prints (section 7). Otherwise the CDN keeps serving the withdrawn files for up to a year.
- **Check:** `kfeed verify https://feed.example.org/` through the CDN checks headers, ranges and that the CDN actually caches (it requests a blob and `feed.json` twice and warns if the second answer isn't from the cache).
- **Cut-off downloads:** CDNs have been seen to end large downloads early without any error. `kfeed sync` notices (size and checksum), repeats the download up to twice per location and tells the player. `kfeed verify --full` warns when a file was only correct on a later attempt, so you notice the problem.
- **If your server is down,** the CDN keeps serving everything it has cached, and `feed.json` is kept for up to a day thanks to `stale-if-error` (Cloudflare and bunny.net support this). Players notice nothing until a file is requested that the CDN doesn't have yet.

## 6. Check your setup

```
kfeed verify https://feed.example.org/
```

It downloads the feed like a game would and prints problems. `ERROR` lines mean games will reject something; `warning` lines mean it works but could be better.

| Message | Fix |
|---|---|
| `feed.json: HTTP 404` (or another status) | Wrong URL or wrong document root. |
| `catalog …: size or hash does not match … (the server sends Content-Encoding …)` | The server labels `.json.gz` files as compressed content. Remove that (`RemoveEncoding .gz` in Apache; no `gzip_static` in nginx). |
| `blob …: is N bytes, the catalog says M` / `content does not match its hash` | The file was changed or cut off in transit or upload. Upload again; look for a CDN feature that rewrites files. |
| `signature …: HTTP 404` | The `sig/` folder wasn't uploaded, or `feed.json` was uploaded from a different build than the rest. Upload the whole folder again. |
| `the signature is not valid` | `feed.json` on the server differs from what was signed. Upload again; never edit it on the server. |
| `served without 'Access-Control-Allow-Origin: *'` | Add the header (§4). |
| `feed.json has no Cache-Control header` | Add `max-age=300` for `feed.json` (§4). |
| `blobs are served without a long Cache-Control` | Add the immutable header for `blobs/`, `catalog/`, `sig/` (§4). |
| `the server ignores range requests` | Enable byte-range support; with a CDN, check that it passes `Range` through. |
| `served over plain http` | Use HTTPS for anything public. |

`kfeed verify <url> --full` downloads and checks every file. Use it after the first upload and after changing CDN settings; it transfers the whole feed once.

## 7. Takedown (a song must be removed)

The operator withdraws the song and builds; the report ends with a list like:

```
Withdrawn files. Delete them on every server and mirror, and purge them from CDN caches:
  blobs/3f/3f9a…
  blobs/a1/a1c0…
```

As the admin:

1. Upload the new build with the script in §3. Step 3 of the script deletes the files from the server.
2. **Purge these URLs from the CDN cache.** Immutable files stay cached for up to a year otherwise.
3. Pass the list to everyone who runs a mirror.
4. Confirm: each URL returns 404, and `kfeed verify <url>` is clean.

Games learn about the withdrawal from `feed.json`, which is why its short cache time matters.

## 8. Mirrors and local servers

**A mirror** is a complete copy of the folder at another URL. Copy it with the script in §3 (from the origin or from the operator), apply the same server configuration, and ask the operator to add your URL to `mirrors` in their settings. Games check every file from a mirror against the checksums of the original feed, so a mirror cannot alter content. Mirror signed feeds only.

**At a party or convention** without reliable internet, any laptop can serve a copy to the local network:

```
kfeed serve /path/to/public
Serving /path/to/public - subscribe on this network with:
  http://192.168.1.20:8080/feed.json
```

Open the port in the laptop's firewall. This uses plain HTTP, which is acceptable here only because signed feeds are verified by signature; don't offer unsigned feeds this way.

## 9. Private feeds (members only)

A feed can be limited to certain people: members of a club, paying subscribers, testers. The feed format has no logins. Instead, **every person gets a personal address** with a random token in it:

```
https://feed.example.org/s/k3VqZ0xLm2Rt7WpY4sHd9AbC/feed.json
```

The server serves the feed under such an address only if the token is on its list. Games and `kfeed sync` need no support for this: they build the address of every file from the address the player entered, so the token is part of every request.

The operator sets `"private": true` in the feed's settings (operator guide, section 11). The rest is your part.

### 9.1 Setting it up

1. Switch the configuration from `hosting/` to private mode; each file says which lines to change. From then on, every request without a listed token gets `403`, also the plain `https://feed.example.org/feed.json`.
2. Make a token for each person: `openssl rand -base64 24 | tr '+/' '-_'` gives 32 random characters. A token consists of letters, digits, `-` and `_`, and is 22 to 128 characters long.
3. Put the tokens into the token list of your server:

   | Server | File | One line per token | After a change |
   |---|---|---|---|
   | nginx | `/etc/nginx/feed-tokens.map` | `the-token 1;` | `nginx -s reload` |
   | Caddy | `/etc/caddy/feed-tokens` | `the-token 1` | `caddy reload` |
   | Apache | `/etc/httpd/feed-tokens.txt` | `the-token 1` | nothing, the file is read again by itself |

   Downloads that are running continue through a reload. The file must be readable by the web server and should not be readable by other users.
4. Give each person their address: `https://feed.example.org/s/<token>/feed.json#ed25519=<key of the feed>`. Hand out `https://` addresses only.
5. To end someone's access, remove their line. If an address got out, give that person a new token and remove the old one; they subscribe again with the new address into the same folder.
6. Check: `curl -I https://feed.example.org/feed.json` must give `403`, and `kfeed verify https://feed.example.org/s/<token>/` must be clean.

The list is a plain file so that anything can write it: a script, a shop system, a members' area that shows the address after a login. Where the login happens and how people pay is up to you; none of it touches the feed. If thousands of people come and go every day, look the token up in your own system instead of a file (nginx `auth_request`, Caddy `forward_auth`). The examples don't cover that.

### 9.2 What it protects, and what it doesn't

- It controls **who can download**. Songs a person already has stay with them when their access ends. It is not copy protection.
- **A personal address works like a password.** Whoever has it can download. Treat the token list the same way.
- The examples keep no access log, so you can't see whether an address is being shared. If you need that, you have to log or count per token yourself; weigh it against the privacy of your members.
- The error log can contain personal addresses, for example of requests that were refused by a rate limit. It is deleted after 14 days (4.2); keep it readable for administrators only.
- The web page of the feed is served under the personal address too. It tells the visitor not to pass the address on, and its links don't reveal the address to other websites.

### 9.3 With a CDN

Don't put a caching CDN in front of personal addresses. For a cache, every person's address is a different file, so nothing would be shared between players; and in private mode the server marks its answers as `Cache-Control: private`. The CDN mode from 5.1 is meant for public feeds.

Instead, let a CDN or a file store deliver only the big files:

1. Upload the folder `blobs/` to the file store under its own host name, for example `https://cdn.example.org/blobs/…`. Upload it there first, then the rest to your server, in the order of section 3.
2. Switch on the optional line in the configuration that sends downloads of big files on to that address.
3. Your server then checks the token and answers each request for a big file with a redirect. `feed.json`, signatures, song lists and the web page still come from your server.

Whoever knows the checksum of a file can then fetch it from the file store without a token. The checksums are in the song lists, which stay behind the token. For a takedown (section 7), delete the files in the file store as well.

## 10. Operations

- **Backups:** the published folder can be regenerated by the operator at any time. What must not be lost is the operator's songs folder, settings and keys, which are not on the web server.
- **Disk space:** the size of `public/` (the build report shows it per category). Keep headroom for one update's worth of new files, because old files are deleted last.
- **Logs and privacy:** games send no identifiers. The examples keep no access log and delete error logs after 14 days (4.2). If you turn an access log on for debugging, turn it off again and delete it afterwards.
- **Monitoring:** watch that `https://…/feed.json` returns 200. A weekly scheduled `kfeed verify <url>` catches expired certificates, broken headers and incomplete uploads.
- **Moving to a new address:** keep the old URL working as long as you can. For a signed feed the operator can announce the new address in the feed itself (`moved_to` in their settings) and games follow automatically; for an unsigned feed players have to subscribe again.

## 11. Checklist

- [ ] HTTPS on a host name you control
- [ ] Configuration from `hosting/` adapted; headers from §2 in place
- [ ] Error log folder created, logrotate file in place (nginx, Apache); no access log (4.2)
- [ ] Rate limits fit your setup: per IP on a single server, or the CDN's limits behind a CDN (4.1)
- [ ] Operating system and web server update automatically
- [ ] Nothing in the path rewrites or re-compresses files (server modules, CDN features)
- [ ] Upload script uses the three-step order
- [ ] `kfeed verify <url> --full` ran clean once
- [ ] You know how to purge single URLs from your CDN
- [ ] Private feed only: the plain address gives `403`, and the token list is not readable by other users (9)
