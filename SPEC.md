# Karaoke Feed Format — draft v0.3 (2026-11-15)

Status: **working draft**, not published. Key words MUST, SHOULD, MAY as in RFC 2119.
License: MIT (see `LICENSE`). Anyone may implement this format, including in commercial software.
Background and reasoning: `DESIGN.md`.

## 1. Purpose and scope
A karaoke feed is a set of **static files** that lists UltraStar-format songs and their media, so that games and tools can browse, download on demand, cache and update them. A feed works from any HTTP(S) server, CDN, LAN server, USB stick or local folder.

A feed can also carry other content for the same games, for example themes, in catalogs of another **kind** (§4.4). Clients that only handle songs ignore them.

Out of scope: the UltraStar txt format itself, highscores, uploads by clients, any server-side code.

Terms:
- **Location**: a base URL (or folder) that holds a copy of the feed. `<base>` is the location of the directory containing `feed.json`.
- **Subscription**: what a client stores per feed: its locations, its **trust set** (public keys; empty = unsigned feed), and the last `feed.json` it accepted.

## 2. Layout
```
<base>/feed.json                          mutable, small, the only file that changes in place
<base>/sig/<sha256 of feed.json>.sig      immutable, optional: Ed25519 signature of that exact feed.json
<base>/catalog/<sha256>.json[.gz]         immutable catalog files
<base>/blobs/<sha256[0:2]>/<sha256>       immutable content files (txt, audio, video, images, license texts)
```
- All paths in this spec are **relative to `<base>`**. The only absolute URLs in a feed are `mirrors`, `moved_to`, `homepage`, `operator.contact` and the `url`/`source` fields of licenses. Only `mirrors` and `moved_to` are ever fetched by a client.
- Hashes are written as `sha256:<64 lowercase hex chars>`. Paths use the bare hex part. A hash always covers the **stored bytes** of the file (for `.json.gz`: the compressed bytes).
- Everything except `feed.json` is **immutable**: a given path always has the same bytes. Hosts SHOULD serve immutable files with `Cache-Control: public, max-age=31536000, immutable`, and `feed.json` with a short max-age (≤ 300 s).
- Publishing order for operators: blobs, then catalogs, then the signature, then `feed.json` last. Old catalogs SHOULD stay available for at least a day.
- Hosts SHOULD send `Access-Control-Allow-Origin: *` on all feed files (web clients).
- Hosts MUST NOT label a stored file with a `Content-Encoding` (for example a `.json.gz` catalog as `Content-Encoding: gzip`): HTTP libraries would unpack it and the hash would no longer match. Ordinary on-the-fly HTTP compression is fine.
- All JSON files MUST be UTF-8 without BOM. Strings SHOULD be in Unicode NFC.

## 3. `feed.json`
```json
{
  "spec": "karaoke-feed/0.3",
  "name": "UltraStar Deluxe CC Songs",
  "description": "Creative Commons songs collected by the UltraStar Deluxe project",
  "homepage": "https://github.com/UltraStar-Deluxe/songs",
  "operator": { "name": "UltraStar Deluxe Team", "contact": "mailto:copyright@usdx.eu" },
  "sequence": 1794650400,
  "generated": "2026-11-14T12:00:00Z",
  "keys": ["ed25519:<base64url of 32 bytes>"],
  "mirrors": ["https://mirror.example.org/usdx-cc/"],
  "removed": ["withdrawn-song-id"],
  "catalogs": [
    {
      "id": "creative-commons",
      "kind": "songs",
      "names": { "en": "Creative Commons", "de": "Creative Commons" },
      "path": "catalog/<hex>.json.gz",
      "sha256": "sha256:<hex>",
      "size": 13120,
      "songs": 46,
      "bytes": 282527753
    }
  ]
}
```
| Field | Req. | Meaning |
|---|---|---|
| `spec` | MUST | `karaoke-feed/<major>.<minor>`. Clients MUST ignore unknown fields. Clients MUST reject unknown majors. While the major is 0 there is no compatibility promise: clients MUST also reject unknown minors. |
| `name`, `operator.name` | MUST | Display names. |
| `operator.contact` | MUST | `mailto:` or `https:` URL for questions and **takedown requests**. |
| `sequence` | MUST | Integer, higher with every published change. Generators SHOULD use the Unix time (seconds) of generation, so they need no stored state. Clients treat it as an opaque counter (§5.1). |
| `generated` | MUST | RFC 3339 UTC timestamp. Display only ("last updated"); never used for ordering. |
| `keys` | MAY | 1 to 8 public keys allowed to sign this feed. See §6. |
| `mirrors` | MAY | Other locations serving an identical copy. `http(s)` URLs without query, fragment or user info. |
| `moved_to` | MAY | `https` URL of the new `feed.json` if the feed moved. See §6.4. |
| `removed` | MAY | Ids of songs the operator **withdrew** (takedown), see §5.4. A withdrawn id MUST NOT be reused. If an id is listed in a catalog and in `removed`, the catalog wins. Operators MAY drop an entry after one year. The list is in `feed.json` and not in the catalogs, so every subscriber sees it, whatever catalogs they chose. |
| `catalogs` | MUST | One or more catalog references. A catalog is the unit a user subscribes to and downloads. `kind` (MUST) is `songs` or the name of another kind, `[a-z0-9][a-z0-9-]{0,31}` (§4.4). `path` ends in `.json` or `.json.gz`. `songs` = number of entries of a `songs` catalog, `items` = number of entries of a catalog of another kind; `bytes` = total size of all distinct blobs the catalog references (size of a full mirror). |

**Catalogs partition the feed:** every song `id` (and every item `id`, §4.4) appears in exactly one catalog; songs and items share one id space and `removed`. Cross-cutting groupings (duets, seasons, languages) are expressed with `edition`, `tags`, `genre` and `language`, not with extra catalogs. If a client finds an id in two catalogs it uses the one listed first.

## 4. Catalog files
```json
{
  "spec": "karaoke-feed/0.3",
  "id": "creative-commons",
  "kind": "songs",
  "songs": [ <song>, ... ],
  "moved": { "old-song-id": "new-song-id" }
}
```
- `kind` (MUST) is the same as in the catalog reference; a client MUST reject a catalog whose `kind` differs.
- `moved` (MAY) maps renamed song ids to their new id, so playlists and statistics survive renames.
- A `.json.gz` catalog is a single gzip member. Clients MUST support gzip and MUST stop decompressing above a limit of their choice (at least 64 MiB).

### 4.1 Song entry
One entry per **chart** (one UltraStar txt file).
```json
{
  "id": "jonathan-coulton-code-monkey",
  "artist": "Jonathan Coulton",
  "title": "Code Monkey",
  "rendition": null,
  "language": ["English"],
  "year": 2006,
  "genre": ["Rock"],
  "edition": "Creative Commons",
  "tags": [],
  "creator": ["SKillMaster"],
  "age": null,
  "duration": 215.3,
  "voices": 1,
  "preview_start": 42.0,
  "chart_hashes": { "play-score-v1": "…" },
  "files": [ <file>, ... ]
}
```
| Field | Req. | Meaning |
|---|---|---|
| `id` | MUST | Operator-assigned, stable for the life of the song. `[a-z0-9][a-z0-9._-]{0,63}`. Unique within the feed. |
| `artist`, `title` | MUST | **Exactly** as in the txt (`#ARTIST`, `#TITLE`), because most games match highscores on artist + title. |
| `rendition`, `language`, `year`, `genre`, `edition`, `tags`, `creator` | SHOULD (if in txt) | Copied from the txt headers; multi-value headers become arrays. |
| `age` | MAY | Minimum recommended age in years, same meaning as the `#AGE` tag agreed in format#96. |
| `duration` | SHOULD | Seconds of singable audio. For UI and size estimates. |
| `voices` | SHOULD | 1 for solo, 2+ for duets. |
| `preview_start` | MAY | Seconds; copied from `#PREVIEWSTART`. Often absent; clients need their own default. |
| `chart_hashes` | MAY | Known score-identity hashes computed by games (namespaced keys). Not used for integrity. |
| `files` | MUST | See 4.2. Exactly one file with role `txt`. |

There is no version number. A song **changed** when the set of (`name`, `sha256`) pairs of its files differs from what the client stored.

### 4.2 File entry
```json
{ "role": "audio", "name": "Code Monkey.mp3", "sha256": "sha256:<hex>", "size": 3480123,
  "type": "audio/mpeg", "license": <license> }
```
| Field | Req. | Meaning |
|---|---|---|
| `role` | MUST | `txt`, `audio`, `vocals`, `instrumental`, `video`, `cover`, `background`, `preview`, `license`. Clients MUST ignore files with a role they don't know. |
| `name` | MUST | The file name **as referenced from the txt** (e.g. `#MP3:Code Monkey.mp3`, decoded to Unicode), so the txt bytes stay untouched. Safety rules: §5.2. |
| `sha256`, `size` | MUST | Integrity and download size. Blob path: `blobs/<hex[0:2]>/<hex>`. |
| `type` | SHOULD | MIME type. |
| `variant` | MAY | E.g. `480p`, `1080p`, `ogg-160k`. Several files with the same role and different variants are alternatives; clients pick one. |
| `license` | MUST (except role `license`) | License object, see 4.3. Repeated per file on purpose: gzip removes the repetition; a lookup table was measured and gave no gain. |

### 4.3 License object
```json
{
  "spdx": "CC-BY-NC-3.0",
  "url": "https://creativecommons.org/licenses/by-nc/3.0/",
  "attribution": "'Code Monkey' by Jonathan Coulton",
  "source": "http://www.jonathancoulton.com/songdetails/Code%20Monkey"
}
```
| Field | Req. | Meaning |
|---|---|---|
| `spdx` | MUST | SPDX identifier, or `LicenseRef-<name>` if none exists (e.g. `LicenseRef-PublicDomain`), or `NOLICENSE` if the operator does not declare a license (`NOLICENSE` is a value of this format, not an SPDX identifier). |
| `url` | MUST unless `NOLICENSE` | License URL. |
| `attribution` | SHOULD | Credit text to show. |
| `source` | SHOULD | Where the licensed work was obtained (provenance proof). |

### 4.4 Catalogs of other kinds
A catalog whose `kind` is not `songs` lists **items** instead of songs. An item is a set of files that belong together, for example a theme for a game: a settings file and its images.
```json
{
  "spec": "karaoke-feed/0.3",
  "id": "extra-themes",
  "kind": "theme",
  "items": [
    { "id": "blue-theme", "names": { "en": "Blue" },
      "files": [
        { "path": "Blue.ini", "sha256": "sha256:<hex>", "size": 120, "license": <license> },
        { "path": "Blue/background.png", "sha256": "sha256:<hex>", "size": 4096, "license": <license> }
      ] }
  ]
}
```
| Field | Req. | Meaning |
|---|---|---|
| `id` | MUST | Same rule as a song id (§4.1). Unique in the whole feed: songs and items share ids and `removed`. |
| `names` | SHOULD | Display names per language. |
| `files` | MUST | One or more files. |
| `path` | MUST | 1 to 8 parts joined by `/`, at most 400 bytes in total. Every part obeys rules 1 to 4 of §5.2. No two files of one item have the same path compared ASCII-case-insensitively, and no path is a folder of another. |
| `sha256`, `size` | MUST | As for song files (§4.2). |
| `license` | MUST | License object (§4.3) with a declared license: `NOLICENSE` is not allowed for items. |

The kind name says what the files are for. This format defines no kind names; a game and the feeds made for it agree on them.

## 5. Client rules
### 5.1 Fetching and verification
1. Fetch `feed.json` (conditional GET with ETag/If-Modified-Since SHOULD be used). At most 1 MiB. Check `spec` and, for signed feeds, the signature (§6).
2. **Rollback:** if `sequence` is lower than in the last accepted `feed.json` of this subscription (or equal with different bytes), the client MUST ignore the fetched `feed.json` and keep using the one it has. It MAY still use that location for catalogs and blobs. This is what makes an older LAN or USB copy usable: it serves the files it has, and it cannot take the client back in time. For unsigned feeds this protection is best effort only.
3. Fetch a catalog only if its `sha256` changed. Verify `size` and hash before parsing.
4. Blobs: download from any location of the subscription or any mirror. Clients that **store** files MUST verify `size` and `sha256` before the file enters their library, and MUST discard it on mismatch. Clients MAY play a file while it downloads (unverified until complete). Clients that only stream MAY skip verification.
5. Clients MUST abort any download that delivers more bytes than the declared `size`.
6. Clients SHOULD spread automatic refreshes randomly (no fixed clock times).
7. A `404` for a catalog or blob is a normal race (the feed changed meanwhile): skip and retry later, don't crash.
8. On `429` or `503` clients MUST slow down: honour `Retry-After` if present, otherwise wait at least 10 s, doubling the wait on each further refusal. Servers may also refuse a connection outright when a client opens too many at once.
9. A `feed.json` that can't be fetched, for whatever reason, changes nothing: the client keeps its library and the `feed.json` it has. On `401` or `403` the client SHOULD tell the user that access was refused (§5.5).

**URLs and privacy.** Clients build every URL as `<location>` + relative path and add nothing: no query string, no cookies, no credentials, no identifiers. Clients MUST follow HTTP redirects (`301`, `302`, `303`, `307`, `308`), also to other hosts, SHOULD give up after 5 in a row, and MUST NOT follow a redirect from `https` to `http`. A redirect applies to that one request: its target never becomes a location.

A location can be personal (§5.5). Clients MUST NOT send a location to anyone but the server it names: no `Referer` header (also not when following a redirect), no telemetry, no crash reports. Clients SHOULD NOT show more than its host where others can see it: in logs, in error messages, on a screen shared at a party. Clients that store locations SHOULD protect them like passwords.

### 5.2 Writing to disk (native clients)
The client chooses the song folder name and location (its own per-user cache). The only string from the feed that becomes part of a path is the file `name`, and only if **all** of these hold:
1. 1 to 160 bytes of UTF-8.
2. None of `/ \ : * ? " < > |`, no control characters (U+0000–U+001F, U+007F).
3. Does not start with `.` or a space, does not end with `.` or a space. (Names starting with `.` are reserved for the client's own files.)
4. The part before the first `.` is not a Windows device name, compared ASCII-case-insensitively: `CON`, `PRN`, `AUX`, `NUL`, `COM0`–`COM9`, `LPT0`–`LPT9`.
5. The extension is one the client knows as media for that role. Baseline: `txt` → `.txt`; `audio`/`vocals`/`instrumental`/`preview` → `.mp3 .ogg .opus .m4a .flac .wav`; `video` → `.mp4 .webm .mkv .avi .mpg .mov`; `cover`/`background` → `.jpg .jpeg .png .webp`; `license` → `.txt .md`. A client MUST NOT store a file with an extension outside its own list.
6. No other file of the entry has the same name, compared ASCII-case-insensitively.

If a name fails, the client MUST skip the **whole entry**. Generators MUST emit names in NFC and MUST NOT emit two names in one entry that differ only by case or Unicode normalization.

Further rules:
- Clients MUST NOT run any downloaded file and MUST NOT need administrator rights.
- A stored file whose hash no longer matches what the client stored was **changed locally** (e.g. by a game's song editor). Clients MUST NOT overwrite or delete it without asking the user.
- Clients SHOULD keep a record per song (subscription, id, file hashes) so updates only fetch changed blobs. Desktop clients MAY store it as a hidden sidecar file (name starting with `.`) in the song folder.
- Entries that share media (e.g. a solo and a duet chart of one song) MAY be written into one folder when every same-named file has the same hash. Otherwise each entry gets its own folder.

### 5.3 Licenses
- Clients SHOULD show license and attribution for the files being played and SHOULD let users filter by license properties (e.g. hide NonCommercial or NoDerivatives). The spec makes no legal judgement between declared licenses.
- **Undeclared licenses:** by default clients SHOULD hide entries that have any file with `NOLICENSE`, until the user opts in. Feeds that games ship as defaults MUST NOT contain `NOLICENSE`.

### 5.4 Removal and renames
- An id that is simply **no longer listed**: the client hides the song and keeps cached files (they may be cleaned up by its normal cache policy). Absence never deletes anything, because it is also what a broken generator run looks like.
- An id listed in `removed` in the `feed.json` of a **signed** feed: the operator withdrew it. The client MUST remove the song from its library and delete its cached files, except files that are also part of another entry and files changed locally (§5.2). Clients SHOULD tell the user which songs were withdrawn.
- `removed` in an unsigned feed is treated like absence (hide).
- Ids listed in `moved`: transfer local state (playlists, cache records) to the new id.

### 5.5 Private feeds
An operator can limit a feed to certain people (members, subscribers, testers) without any addition to this format. Each of them gets a **personal location**, for example `https://feed.example.org/s/<token>/`, where `<token>` is a random string of at least 128 bits that the server checks on every request. Because clients build every URL from the location (§5.1), the token travels with every request, in every client. How people get their location (for example after a login on the operator's website) is outside this spec.

- A private feed MUST be signed. Only then can a subscriber move to a new personal location when a token is replaced: it is added like any other location of a signed feed (§6.3).
- Its `feed.json` is the same file for every subscriber, so it MUST NOT contain `mirrors` or `moved_to`.
- The server MAY answer requests for blobs with a redirect, for example to a CDN that serves the blobs under their hashes without a token. Whoever knows a hash can then fetch that blob, so the catalogs stay behind the token.
- The server refuses an unknown or withdrawn token with `403` (or `404`). For the client that is a failed fetch (§5.1, rule 9): the library stays as it is. Ending someone's access does not remove the songs they already have. This is access control for downloads, not copy protection.
- Clients need no special support, but they treat every location as possibly personal (§5.1, "URLs and privacy").

### 5.6 Other pack kinds
1. A client MUST ignore catalogs of a kind it does not handle. A game that only plays songs ignores everything but `songs`.
2. A client MUST install another kind only from a **signed** feed whose key came with the client's own configuration (§6.2: keys built into the game). Not with a key learned on first use, and never from an unsigned feed.
3. The folder for a kind and the list of allowed file extensions come from the client's configuration, never from the feed. A file is written to `<folder>/<path>`.
4. A client MUST NOT store a file whose extension is not in that list, and never one of: `.exe .dll .com .bat .cmd .msi .scr .pif .ps1 .vbs .js .jar .sh .so .dylib .lnk .desktop .app .apk .reg`. If one path of an item fails, the client MUST skip the whole item.
5. A file at one of an item's paths is replaced with the feed's version, also when the client did not write it or it was changed locally: it is the same item, installed or edited by hand (unlike song files, §5.2). Files at other paths are never changed or deleted. If two items want the same path, or a file of one is a folder of the other, the first one in feed order (catalogs, then items) wins and the second item is skipped. A file another item now uses is not deleted for the item that dropped it.
6. An item that is simply no longer listed keeps its files. For an id in `removed`, and for an item of a catalog the user no longer chooses, the client deletes the files it wrote and that are unchanged.
7. "Clients MUST NOT run any downloaded file" (§5.2) applies to every kind.

## 6. Signatures (optional for clients, see 6.5)
### 6.1 Format
- Algorithm: **Ed25519** (RFC 8032) over the exact bytes of `feed.json`.
- The signature of a `feed.json` whose SHA-256 is `<hex>` is stored at `sig/<hex>.sig`: 64 bytes, base64url without padding, optional trailing newline, at most 1 KiB. Because the path is derived from the content, a client can never combine a new `feed.json` with an old signature.
- `keys` lists the public keys as `ed25519:<base64url of 32 bytes>`. A signature is valid if it verifies under **any** key of the client's trust set.
- Because `feed.json` contains the catalog hashes and catalogs contain the blob hashes, one signature covers the whole feed, on every location.

### 6.2 Trust set
- A subscription gets its first trust set from, in this order: keys built into the game (default feeds); a key in the **subscription string** `https://host/path/feed.json#ed25519=<base64url public key>`; or, if neither exists, the `keys` of the first `feed.json` it accepts whose signature verifies under one of those keys (trust on first use).
- Whenever a `feed.json` verifies under the current trust set, the client replaces the trust set with that file's `keys`. This is the whole **key rotation** mechanism: publish `[old, new]` signed by `old`, later `[new]`. Operators SHOULD list a second, offline-stored key from the start and SHOULD keep an old key listed for at least a year.
- A subscription with a non-empty trust set MUST reject a `feed.json` with a missing or invalid signature, or without `keys`, and keep using the one it has.
- Known limit: whoever steals a signing key can rotate the others out. Subscribers who miss a whole rotation, and subscribers of a feed that lost all keys, must subscribe again.

### 6.3 Signed and unsigned feeds
| | Signed (trust set not empty) | Unsigned |
|---|---|---|
| `feed.json` may come from | any location: origin, mirror, LAN server, USB stick, plain `http` | **only** the subscribed URL; clients SHOULD warn if that URL is not `https` or a local file |
| Catalogs and blobs may come from | any location or mirror | any location or mirror (they are hash-checked against the `feed.json` above) |
| Rollback protection | yes | best effort |
| `removed` deletes cached files | yes | no (hide only) |
| `moved_to` followed automatically | yes | no (shown to the user) |

A user adds a LAN or USB copy as another location. If its `feed.json` verifies under the trust set of an existing subscription, the client adds the location to that subscription; otherwise it is a new subscription.

### 6.4 `moved_to`
Only from a signed feed: the client fetches the new `feed.json`, which must verify under the current trust set, and replaces the subscription's locations.

### 6.5 Who must do what
- Feeds that games ship as **defaults** MUST be signed, and the game SHOULD ship more than one location for them.
- Signature checking is optional for clients. A client that cannot check signatures treats every feed as unsigned (§6.3) and SHOULD show it as unverified.

## 7. Previews
Clients SHOULD play previews with HTTP range requests on the audio blob, starting near `preview_start`. A server may answer a range request with the full file (`200`); clients MUST handle that (fall back to a normal download or skip the preview). Operators MAY provide a short file with role `preview` for clients that can't seek remotely. For MP4/M4A audio, operators SHOULD publish files with the index at the front ("faststart"), otherwise range previews don't work.

## 8. Versioning of this spec
From 1.0 on, minor versions only add optional fields, and a major version change means old clients must refuse the feed. Before 1.0 every minor version may break (see `spec` in §3).
