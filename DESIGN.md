# Design notes

Why the karaoke feed format and its tools look the way they do. The normative rules are in [SPEC.md](SPEC.md); this file gives the reasons.

## The problem

Legal karaoke content for UltraStar-format games exists: Creative Commons songs, fan productions, community charts. But there is no way to subscribe to it.

- **Players** copy song folders by hand, and get no updates or fixes. On phones, TV boxes and Steam Decks there is little space for full libraries.
- **Earlier distribution attempts** died with their single host. The old USDX installer downloaded CC songs from SourceForge without checksums, broke during a SourceForge outage and was removed (USDX#109). Other projects used archives on one server, or a hard-coded storage bucket.
- **The demand is documented:** "remote song repositories" (USDX#608, open since 2022), song folders over the network for a Fire TV with little storage (Play#347), streaming media from URLs without any caching (Play#278).
- **The format community has twice decided against new metadata in the txt files:** license headers (format#23) and MusicBrainz ids (format#19). So license and identity information has to live outside the txt.

## Goals

- **Players:** subscribe once, pick categories, songs arrive and stay up to date, offline at a party.
- **Feed operators:** a folder of songs in, static files out. No server code, cheap to host, takedowns in minutes.
- **Game developers:** a client is doable in a weekend in Pascal, C#, C++ or JavaScript with JSON, HTTP and SHA-256. No game has to change for the first version.
- **Longevity:** static files, boring technology, and no dependency on a single host or domain.
- **Legal clarity:** a license on every file, visible and filterable; a takedown contact for every feed.

## Decisions and reasons

| Decision | Reason |
|---|---|
| **Static files only:** one small mutable `feed.json`, everything else immutable and named by its SHA-256 | Works from any web server, CDN, LAN laptop, USB stick or `file://`. Immutable files can be cached forever, so a cheap server plus a CDN carries peak days. |
| **One file per media file, no archives** | Updates fetch only changed files. Interrupted downloads resume per file. No archive extraction, so no zip bombs and no path traversal through archives. |
| **Integrity:** SHA-256 for every file; one optional Ed25519 signature over `feed.json` | The signature covers the whole feed, because `feed.json` lists catalog hashes and catalogs list file hashes. SHA-256 is built into every platform; Ed25519 is in Go, Java, browsers and small portable implementations. |
| **A signed feed's identity is its key, not its domain** | Mirrors, LAN copies and USB sticks are as trustworthy as the original. An expired and re-registered domain can't take over a signed feed. |
| **Signing is optional; feeds shipped as game defaults must be signed** | Small operators can start without key handling; defaults need the protection. |
| **Key rotation via a key list:** any listed key may sign, and clients follow the list | Games ship keys and live for years; a lost key must not kill a default feed. |
| **Rollback protection:** `sequence` only goes up; an older `feed.json` is ignored, but its location still serves files | An old LAN or USB copy stays usable without taking players back in time. |
| **Generators use the Unix time as `sequence`** | No state needed between builds; works in CI. |
| **Signature file named by the hash of `feed.json`** | No race between two mutable files behind a CDN. |
| **File names come from the txt references** and are validated by clients | The txt bytes stay untouched; the validation rules (no path separators, no Windows device names, an extension list per role) keep a hostile feed from writing anywhere else. |
| **One entry per txt chart**; charts can share media | Solo/duet or karaoke/vocal versions of one song are separate entries, as in the games. |
| **Song id assigned by the operator; artist and title copied exactly from the txt** | Playlists survive fixes and renames (`moved` map); the games' highscore databases keep matching, since most of them key on artist and title. |
| **No score hash in the format** | Games already use different, incompatible ones; the feed may carry them as optional metadata. |
| **A license object on every file:** SPDX id, URL, attribution, source | A song's txt, audio and video often have different licenses and authors. `NOLICENSE` is allowed but hidden by default and not allowed in default feeds. The format makes no other legal judgement; clients let users filter (e.g. hide NonCommercial). |
| **Catalogs partition the feed** (one song, one catalog) | A catalog is the unit players subscribe to and download. Cross-cutting groups use the txt headers (`#EDITION`, `#GENRE`, `#TAGS`). |
| **Delisted means hidden, withdrawn means deleted** | A broken generator run must never wipe players' libraries. Only an explicit `removed` list in a signed feed deletes. |
| **Local edits are never overwritten** | Players fix timings in the games' editors; a sync must not destroy their work. |
| **Clients never upload** | No peer-to-peer: simple, no legal grey zone for players, works behind any firewall. |
| **Reference tools in Go** | One static binary per platform with no runtime, and the language's compatibility promise. The real clients are meant to live inside the games; `kfeed` is the generator, the reference client and the test harness. |
| **Downloads are repeated up to twice** when size or checksum don't match | CDNs have been seen to end large downloads early without an error. The checksum catches it; repeating fixes it; `kfeed verify` reports it. |
| **A static web page for every feed**, made by `kfeed build` | Whoever opens a feed address in a browser learns what it is and how to use it. It is self-contained (nothing from other sites, no cookies) for privacy, and served with a policy that allows only its own stylesheet and script. |
| **Private feeds use a personal address** (a random token in the path), not a login in the client | The games are written by independent teams. Anything a client has to implement for access control would work in some games only. A personal address works in every client, because clients already build every URL from the feed address. Accounts, logins and payment stay on the operator's website. Private podcast feeds work the same way. |
| **MIT license** for the format and the tools | Any game, including commercial ones, may implement and ship it. |

## Alternatives considered

| Alternative | Why not |
|---|---|
| `git clone` of a songs repository | Fine as the operator's source, wrong for players: full history, no on-demand downloads, LFS costs. The generator reads such a folder. |
| BitTorrent / IPFS | Players would also upload, which causes confusion and legal worries for players; often blocked at venues; IPFS gateways are unreliable. Possible later as an extra mirror type. |
| One archive per song on a website | No integrity, no updates, no discovery, extraction risks. |
| RSS / Podcasting 2.0 | One enclosure per item, while a song is several files; integrity and license tags are rarely supported. |
| Streaming only | Breaks offline parties and venues with bad Wi-Fi. Supported as a client mode, not as the only one. |
| A login in the client (OAuth, or a token sent as a header) | Every game would have to implement it: a login flow, token refresh, safe storage. A game also has to be known to the operator's login server, which doesn't work for games nobody has registered. A feed protected this way would work in some games only. |
| License headers in the txt | Rejected by the format community (format#23). |

## Open questions

- **Variants:** a file may have alternatives (e.g. video in 480p and 1080p), but the txt references one file name. The spec doesn't yet say how a client picks one.
- **`license.txt` in song folders:** games that read every `.txt` as a song log errors for it. USDX could skip files named `license.txt`, or feeds could publish license files under another name.
- **A JSON Schema** for `feed.json` and catalogs.
- **Integration into the games:** an in-game feed browser, downloads on demand while playing, and a cache size limit. The USDX Flatpak would need network access for that.
- **Where the spec lives in the long run**, and who runs the first official feed.
