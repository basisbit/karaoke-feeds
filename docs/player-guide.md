# Getting songs from a karaoke feed: player guide

`kfeed sync` downloads the songs of a feed into a folder your karaoke game reads, and keeps them up to date. It works with any game that reads UltraStar song folders (UltraStar Deluxe, Vocaluxe, Performous, UltraStar Play and others); the game itself needs no change.

## 1. Choose a folder

Use a **new, empty folder inside your game's songs folder**, one folder per feed. `kfeed sync` manages everything in it; don't put your own songs there.

| Game / installation | Songs folder |
|---|---|
| UltraStar Deluxe on Linux | `~/.ultrastardx/songs/` |
| UltraStar Deluxe from Flathub | `~/.var/app/eu.usdx.UltraStarDeluxe/.ultrastardx/songs/` |
| anything else | the songs folder of your installation, or any folder you add to USDX's `config.ini` under `[Directories]` as `SongDir1=…` |

Example: `~/.ultrastardx/songs/usdx-cc/`

## 2. First sync

```
kfeed sync https://feed.example.org/feed.json#ed25519=AbC… ~/.ultrastardx/songs/usdx-cc
```

Use the address exactly as the feed operator publishes it. The part after `#ed25519=` is the feed's key: with it, `kfeed` checks that every song really comes from that operator, wherever it is downloaded from.

The first run lists the feed's categories with their sizes and stops. Run it again with your choice:

```
kfeed sync https://feed.example.org/feed.json#ed25519=AbC… ~/.ultrastardx/songs/usdx-cc --catalog creative-commons
kfeed sync … --all          # all categories, also ones added later
```

Then start (or restart) the game: it finds the new songs when it starts.

## 3. Staying up to date

```
kfeed sync ~/.ultrastardx/songs/usdx-cc
```

The folder remembers the address, your categories and the key. Only files that changed are downloaded. Run it before a party, or regularly (a weekly scheduled task is fine). Running it twice at the same time is prevented.

What happens to your songs:

| In the feed | In your folder |
|---|---|
| new song | downloaded; appears in the game when all its files are complete |
| song fixed (e.g. timing) | only the changed files are downloaded |
| song renamed by the operator | the folder is renamed; playlists that use artist and title follow the new names |
| song no longer offered | **hidden**: its `.txt` is renamed to `….txt.kfeed-hidden`, so the game ignores it. The files stay. If it comes back, it is shown again without a new download. |
| song withdrawn by the operator (takedown) | deleted (only for signed feeds) |
| you remove a category from your choice | its songs are deleted |

**Songs you changed yourself** (for example timing fixed in the game's editor) are never overwritten or deleted. `kfeed sync` reports them as "kept as they are". To take the feed's version instead, add `--replace-local`.

**Songs without a declared license** are not downloaded by default; the report lists them. `--include-undeclared` downloads them as well.

**Leaving out license types.** `--exclude nc` leaves out songs under a NonCommercial license, `--exclude nd` NoDerivatives, `--exclude sa` ShareAlike; combine them as `--exclude nc,nd`. Songs already in your folder that match are removed (except files you changed). The choice is remembered; `--exclude ""` switches it off again. This matters for example for events with an entrance fee: whether NonCommercial songs may be played there is a legal question the tool can't answer, but it lets you leave them out.

## 4. Without internet: LAN and USB copies

A copy of a signed feed works like the original:

```
kfeed sync http://192.168.1.20:8080/feed.json ~/.ultrastardx/songs/usdx-cc     # a laptop running "kfeed serve"
kfeed sync /media/usb/my-feed ~/.ultrastardx/songs/usdx-cc                     # a USB stick
```

`kfeed` accepts the copy only if it is signed with the subscribed key, and it never goes back to an older state of the feed. Copies of **unsigned** feeds are refused; they can only be synced from their original address.

## 5. Private feeds: your personal address

Some feeds are for members or subscribers only. Their operator gives you a personal address with a long random part:

```
https://feed.example.org/s/k3VqZ0xLm2Rt7WpY4sHd9AbC/feed.json#ed25519=AbC…
```

It works like any other feed address, but it is yours alone. Treat it like a password: don't pass it on and don't post it.

To keep it out of your shell's history, put `-` in place of the address. `kfeed` then asks for it:

```
kfeed sync - ~/.ultrastardx/songs/members-feed --all
Feed address: (paste the address and press Enter)
```

Later updates need only the folder, as usual. `kfeed` keeps the address in the folder's `.kfeed` directory, readable only by you, and shows only the host name in its messages.

- **If your access ends,** `kfeed sync` reports "access refused" and leaves your songs as they are.
- **If the operator gives you a new address,** run the command above once more with the new address and the same folder.

## 6. Options

| Option | Meaning |
|---|---|
| `-` in place of the address | `kfeed` reads the address from standard input instead of the command line (section 5) |
| `--catalog <id>` | category to sync; repeat for several. Replaces the previous choice. |
| `--all` | all categories, also future ones |
| `--include-undeclared` | also songs without a declared license (`--include-undeclared=false` to switch back) |
| `--exclude nc,nd,sa` | leave out songs with these license properties (`--exclude ""` for none) |
| `--key ed25519:…` | the feed's key, if the address doesn't contain it; also a file containing the key (`--key feed.pub`) |
| `--replace-local` | replace files you changed with the feed's version |
| `--jobs <n>` | parallel downloads (default 4) |
| `--target <kind>=<folder>` | where packs of another kind go, for example `--target theme=~/.game/themes`. Repeat for several kinds. Needs `--extensions` for the same kind. |
| `--extensions <kind>=.ini,.png` | the file types allowed in that folder. Programs and scripts (`.exe`, `.sh`, `.js`, …) are never stored. |

Packs of another kind (for example themes, see `kfeed sync` without `--catalog` for the list) are installed only from a **signed** feed whose key you give with `--key` or in the address, and only into a folder you name with `--target`. Without that, `kfeed sync` leaves them alone and says so. A file at the same place as one of the pack's files (for example an older copy of the same theme you installed by hand, or a file of it you changed) is replaced with the feed's version. Other files in that folder are never touched. When you stop choosing such a pack, the files `kfeed` wrote are deleted; files you changed since the last sync, and files you added, stay.

## 7. Good to know

- The folder `.kfeed` inside the sync folder holds the state. Deleting it makes `kfeed` forget the subscription; the songs stay, but are then treated as files that aren't its own.
- Interrupted downloads leave nothing broken: files are checked and only then moved into place, and a song's `.txt` is written last.
  A file that was cut off halfway is kept as a hidden `.kfeed-part-…` file and continued from where it stopped on the next
  attempt or the next sync (if the server supports range requests; otherwise it is downloaded again). Leftover part
  files are deleted once the song is complete or removed.
- Every file is checked against its checksum before it enters your library. A mismatch is reported and the file is discarded.
- Not available yet: a size limit for the folder, downloading only on demand while playing, and a graphical interface. These belong into the games themselves; `kfeed sync` is the version that works today without changing any game.
