# Signing a feed: setup and maintenance

This guide is for the feed operator. It covers creating keys, daily use, replacing keys and what to do when one is lost or stolen.
Related guides: [operator-guide.md](operator-guide.md), [hosting.md](hosting.md).

## 1. Do you need it?

A signature proves that the feed comes from you, wherever a game got it from.

| | Unsigned feed | Signed feed |
|---|---|---|
| Someone takes over your domain or server | they can serve players anything | games reject it and keep the last good version |
| Mirrors, a laptop at a party, a USB stick | games only trust your original web address | any copy is as trustworthy as the original |
| Withdrawn songs (takedown) | hidden in games | also deleted from players' devices |
| Shipping as a default feed in a game | not allowed | allowed |
| Effort | none | two key files to look after |

For a private feed among friends, unsigned is fine. For a public feed, sign it. **Decide before you publish the address:** games that subscribed while the feed was unsigned stay "unsigned" until the player subscribes again, and a signed feed can never go back to unsigned (see §7).

## 2. The idea in four sentences

- A **signing key** is a small secret file. Whoever has it can publish in your name.
- Its **public key** is a line of text starting with `ed25519:`. It is not secret; it goes into `feed-config.json` and into the link you give to players.
- A feed lists up to 8 public keys, and a signature by **any one** of them is accepted. That is what makes a spare key and a later key change possible.
- Games remember the key list from the last feed version they accepted.

## 3. First setup

Create two keys: one for daily use, one spare that you lock away.

```
kfeed keygen feed.key
kfeed keygen backup.key
```

Each command writes the secret file and prints the public key:

```
Private key written to feed.key. Keep it secret and keep a backup.
Public key (add it to "keys" in feed-config.json):
ed25519:t2pehF49QwpSi6lPiU8kmefnMcRHS7UiGRxMfHumsvA
```

Then:

1. Put **both** public keys into `feed-config.json`:
   ```json
   "keys": [
     "ed25519:t2pehF49QwpSi6lPiU8kmefnMcRHS7UiGRxMfHumsvA",
     "ed25519:j1zNOadKXvo08pzPovRbVlbzEbkM8gBjzzovcnN5OLE"
   ]
   ```
2. Move `backup.key` to a place that is **not** the computer you build on: a USB stick in a drawer, a password manager, an encrypted backup. You will need it on the day the other key is lost or stolen, so make sure you can find it then.
3. Keep `feed.key` on the build computer, outside the feed folder. It must never end up in `songs/`, in `public/`, in a git repository or on the web server.
4. Build with the key and check:
   ```
   kfeed build my-feed --key feed.key
   kfeed verify my-feed/public
   ```
   The build report says `signed by ed25519:…`, and `verify` ends with `OK: … signed, 2 key(s) listed`.
5. Upload `public/` including the new `sig/` folder ([hosting.md](hosting.md)).
6. Give players the feed address with your public key attached, so their game checks the very first download too:
   ```
   https://feed.example.org/feed.json#ed25519=t2pehF49QwpSi6lPiU8kmefnMcRHS7UiGRxMfHumsvA
   ```
   (the part after `ed25519:`, attached with `#ed25519=`). Without the key in the link a game trusts whatever keys it sees on first contact.

If you lose track of which file belongs to which public key: `kfeed pubkey feed.key` prints it again. To keep it in a file: `kfeed pubkey feed.key > feed.pub`. Wherever `kfeed` asks for a public key (`--key` of `kfeed sync` and `kfeed verify`), you can give that file instead of the long text.

## 4. Daily use

Nothing changes except `--key`:

```
kfeed build my-feed --key feed.key
```

The build refuses to run without the key once `keys` is filled in, and it refuses a key that isn't listed. So you can't publish an unsigned or wrongly signed version by accident.

### Where the key comes from

The private key is never written on the command line, so it can't end up in your shell history or in the process list: `--key` takes the **name of the key file**. There are three ways to give it:

| Way | Example | Good for |
|---|---|---|
| a file | `kfeed build my-feed --key ~/keys/feed.key` | your own computer, with the key on an encrypted disk |
| an environment variable with the file name | `KFEED_KEY=~/keys/feed.key kfeed build my-feed` | scripts |
| standard input (`-`) | `pass show karaoke/feed.key \| kfeed build my-feed --key -` | a password manager or a CI secret: the key is never stored unencrypted on disk |

`kfeed` refuses a key file that other users on the computer can read, like OpenSSH does, and tells you the command to fix it (`chmod 600 feed.key`). Files made by `kfeed keygen` are readable only by you from the start. (On Windows, file access is controlled differently and not checked.)

**Recommended:** keep the key in your password manager (KeePassXC, `pass`, Bitwarden, 1Password …) as a text entry, and pipe it in with `--key -`. Then it is encrypted when you don't use it. If you keep a key file instead, keep it on an encrypted disk, outside the feed folder and outside any repository.

The signature covers the whole feed: `feed.json` contains the checksums of the catalogs, the catalogs contain the checksums of every song file. Changing any file in `public/` by hand breaks it, which is intended.

**Automated builds (CI):** store the content of `feed.key` as a secret of the CI system and pipe it in, so it is never written to the build machine's disk. In GitHub Actions:

```yaml
- name: Build the feed
  env:
    FEED_KEY: ${{ secrets.FEED_KEY }}
  run: printf '%s\n' "$FEED_KEY" | kfeed build my-feed --key -
```

Everyone who can change the build job can sign in your name, so restrict who can. The backup key never goes into CI.

## 5. Replacing a key (planned)

Do this when a person with access leaves, when you move to a new build system, or simply every few years.

1. `kfeed keygen new.key`
2. In `feed-config.json`, **add** the new public key to `keys`. Keep the old ones for now.
3. Build with a key that games already know: `kfeed build my-feed --key feed.key`. Upload.
4. From now on build with the new key: `--key new.key`.
5. After **at least a year**, remove the old public key from `keys`, build and upload. Then delete the old key file.

Why wait a year: a game that was switched off the whole time still only knows the old key list. As long as the old key is listed or the feed is signed by a key the game knows, it follows along. A game that missed the complete change must be subscribed again by its player.

## 6. When something goes wrong

**The daily key is lost** (disk died, file deleted):
1. Fetch `backup.key`. Build with it: `kfeed build my-feed --key backup.key`. Games accept it because its public key was listed all along.
2. Create a new daily key, add its public key to `keys`, remove the lost one, build with the backup key once more, upload.
3. Create a new spare key as well (add it the same way) so that you have two again.

**A key was stolen or may have been copied:**
1. Act the same day. Remove the stolen key's public key from `keys`, add a new one, and build with the key that was *not* stolen. Upload.
2. Every game that fetches this version stops accepting the stolen key.
3. Check your server and domain access too: the thief needs a way to deliver files to players (your server, your domain, a mirror) for the key to be useful.
4. Limit you should know: if the thief gets a forged feed version to a game before your corrected one arrives, that game follows the thief's key list. Its player has to subscribe again with the link from §3.6. This is why the daily key and the server should not sit in the same place.

**Both keys are lost:**
There is no recovery, by design. Nobody, including you, can publish a version that existing subscribers accept. Create new keys, build, and tell players (homepage, community channels) to remove the feed and subscribe again with the new link. Their downloaded songs stay usable.

**`verify` says "the signature is not valid" or the signature file is missing:**
- `feed.json` was edited by hand or changed during upload → build again, upload again.
- The `sig/` folder wasn't uploaded → upload it. Order matters: `sig/` before `feed.json` ([hosting.md](hosting.md)).
- You built with a different key than you think → `kfeed pubkey <file>` and compare with `keys`.

## 7. Things you can't undo

- **A signed feed cannot become unsigned.** Games that know your keys reject an unsigned version. Don't empty `keys` on a live feed.
- **Turning signing on later** protects new subscribers only. Games that subscribed while it was unsigned keep treating it as unsigned until the player subscribes again.
- **Never remove all keys that games know in one step.** Always keep at least one known key in the list across a change (§5).

## 8. Technical details

- Algorithm: Ed25519. Key files are PKCS#8 PEM (`-----BEGIN PRIVATE KEY-----`), the same format `openssl genpkey -algorithm ed25519` writes, so keys can be moved between tools.
- Key files are **not** password protected themselves. Keep the key in a password manager and use `--key -` (section 4), or protect the file with disk encryption and by where you keep it. `kfeed` refuses key files that other users can read.
- The signature of a feed version is stored in `public/sig/<checksum of feed.json>.sig`. `kfeed build` keeps the current and the previous one and deletes older ones.
- The exact rules for games are in [SPEC.md](../SPEC.md) §6.

## 9. Checklist

- [ ] Two keys created; both public keys in `feed-config.json`
- [ ] Spare key stored away from the build computer, and you know where
- [ ] No key file inside the feed folder, a repository or on the web server
- [ ] `kfeed verify` reports `signed`
- [ ] The link you publish contains `#ed25519=…`
- [ ] A note in your calendar to test the spare key once a year (`kfeed pubkey backup.key` is enough to see the file still reads)
