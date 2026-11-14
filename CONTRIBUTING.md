# Contributing

Thanks for helping! Bug reports, ideas and pull requests are welcome. For changes to the format, please open a
"Format change proposal" issue first; DESIGN.md explains the decisions so far.

## How changes get in

- `main` is always the current state. Nobody pushes to it directly; every change, including from maintainers, comes
  as a pull request from a branch (`feat/…`, `fix/…`, `docs/…`) or a fork.
- Pull requests are merged **by rebase** only: no merge commits, no squash. So every commit of your branch ends up
  on `main` as it is: keep them tidy and meaningful.
- Before a merge, these checks must pass: tests on Linux, Windows and macOS, code quality (`gofmt`, `go vet`,
  `staticcheck`, `govulncheck`, race detector, `shellcheck`, `actionlint`) and commit messages.
- Keep your branch up to date by rebasing, never by merging main into it:
  ```
  git fetch origin
  git rebase origin/main
  git push --force-with-lease
  ```

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/): `type(optional scope): description`, at most 100 characters.

| Type | Use for | Effect on the next release (before 1.0) |
|---|---|---|
| `feat` | a new feature | minor version (0.**x**.0) |
| `fix` | a bug fix | patch version (0.0.**x**) |
| `feat!` / `fix!`, or `BREAKING CHANGE:` in the body | a change that breaks feeds, clients or libraries | minor version (major after 1.0) |
| `docs`, `perf`, `refactor`, `test`, `style`, `build`, `ci`, `chore`, `revert` | everything else | listed in the changelog (some) or not at all |

Examples: `feat(sync): exclude songs by license type`, `fix(build): keep the BOM of txt files`,
`docs(hosting): explain CDN mode`.

## Releases

Releases are automatic. A bot keeps a release pull request open that collects the commits on `main` into
`CHANGELOG.md` and proposes the next version. Merging it creates the tag and the GitHub release, and the `kfeed`
binaries for Linux, Windows and macOS are attached.

For maintainers, once: the release pull request needs a token that may start workflows, because pull requests
opened with the default token don't get their required checks. Store a fine-grained personal access token (this
repository only; "Contents" and "Pull requests": read and write) or a GitHub App token as the secret
`RELEASE_PLEASE_TOKEN`.

## Dependencies

Dependabot checks weekly. All patch updates come as one pull request that merges itself when the checks are green.
All minor and major updates come as another one that a maintainer reviews. Security fixes are bundled the same way,
as soon as an advisory is published.

## Working on the code

```
cd tool
gofmt -l .            # must print nothing
go vet ./...
go test ./...
```

The tool uses only Go's standard library; please keep it that way unless there is a very good reason.
The web server configurations have their own test, `hosting/test/run.sh` (see the comment at its top).

## Content

Never add song content (audio, video, lyrics, UltraStar txt files) to this repository, except Creative Commons
material with its license, and only where a test really needs it. Test fixtures should use generated dummy files.

## License

By contributing you agree that your contribution is licensed under the MIT license of this repository.
