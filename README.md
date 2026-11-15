# karaoke-feeds

A format and tools for "podcast-style" feeds of karaoke songs in the UltraStar format: a game subscribes to a feed address, shows the song list, and downloads songs on demand or in full. Feeds are plain static files, so any web server, CDN, LAN laptop or USB stick can serve them.

**Status: working draft, not published.** The format is version 0.3 and may still change.

A feed can also carry other content for the same games in packs of another kind, for example themes. Games install those only from signed feeds they were set up with.

| You are | Read |
|---|---|
| a player who wants a feed's songs in the game | [docs/player-guide.md](docs/player-guide.md) |
| running a feed (adding, changing, removing songs) | [docs/operator-guide.md](docs/operator-guide.md) |
| setting up or maintaining signing keys | [docs/signing.md](docs/signing.md) |
| running the web server | [docs/hosting.md](docs/hosting.md) |
| writing a game or tool that reads feeds | [SPEC.md](SPEC.md) |
| interested in why it is designed this way | [DESIGN.md](DESIGN.md) |

## Quick start

```
cd tool && go build -o kfeed ./cmd/kfeed && cd ..      # needs Go 1.22+, no other dependencies; or download a release binary
tool/kfeed init my-feed                       # then edit my-feed/feed-config.json
#   put songs into my-feed/songs/<Category>/<song folder>/
tool/kfeed build my-feed                      # creates my-feed/public/ (with a web page for visitors) and reports what changed
tool/kfeed verify my-feed/public              # checks it like a strict game would
tool/kfeed serve my-feed/public               # serves it on the local network
tool/kfeed sync my-feed/public ~/.ultrastardx/songs/my-feed --all   # player side: songs into the game
```

## What is where

- `SPEC.md`: the feed format.
- `tool/`: `kfeed`, the operator tool (Go, standard library only). Tests: `go test ./...` in `tool/`.
- `docs/`: the three guides above.
- `hosting/`: web server configurations (nginx, Apache, Caddy) and their test suite (`hosting/test/`).
- `example-source/` → `example-feed/`: a prototype feed built from the Creative Commons songs of [UltraStar-Deluxe/songs](https://github.com/UltraStar-Deluxe/songs). Not an official feed.
- `proto/`: throwaway Python prototypes of format 0.2 (they don't know pack kinds), superseded by `kfeed build` and `kfeed sync`; kept as an independent second implementation for cross-checks.
- `DESIGN.md`: decisions, reasons, alternatives and open questions.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) (pull requests, commit messages, releases) and [SECURITY.md](SECURITY.md) (report vulnerabilities privately).

## License

MIT, see [LICENSE](LICENSE): the format specification, the documentation, the `kfeed` tool, the web server configurations and everything else in this repository that was written for it. Anyone may use it, including commercial games and services.
