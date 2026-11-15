// Package kfeed builds, signs, checks and serves karaoke feeds (SPEC.md, karaoke-feed/0.3), and downloads them
// into a library folder. The kfeed command is in cmd/kfeed. Standard library only.
package kfeed

import (
	"flag"
	"fmt"
	"os"
)

const usageText = `kfeed - build, sign, check and serve karaoke feeds (spec karaoke-feed/0.3)

Usage:
  kfeed version                        print the version
  kfeed init <feed-folder>             create a new feed folder with a config template
  kfeed keygen <key-file>              create a signing key and print its public key
  kfeed pubkey <key-file>              print the public key of a signing key ("-": from standard input)
  kfeed build <feed-folder> [flags]    rebuild the published folder from the songs folder
        --key <file>            signing key file (or environment variable KFEED_KEY); "-" reads the key
                                from standard input, e.g. from a password manager: pass show kfeed | kfeed build … --key -
        --out <folder>          published folder (default: "output" in feed-config.json)
        --dry-run               report what would change, write nothing
        --allow-mass-removal    continue although many songs disappeared
        --copy                  copy files instead of hard-linking them
  kfeed verify <folder-or-URL> [flags] check a published feed the way a strict client does
        --key <public key>      require a valid signature by this key (ed25519:... or a file containing it)
        --full                  download and hash every file (default: check sizes)
  kfeed serve <published-folder> [--addr :8080]
                                       serve a feed on the local network with correct headers
  kfeed sync [<feed address> | -] <library folder> [flags]
                                       download a feed's songs into a game's songs folder and keep them updated
                                       ("-" reads the address from standard input: for personal addresses)
        --catalog <id>          catalog to sync (repeatable); --all for all of them
        --include-undeclared    also songs without a declared license (hidden by default)
        --exclude nc,nd,sa      leave out NonCommercial / NoDerivatives / ShareAlike songs
        --key <public key>      the feed's key (ed25519:... or a file containing it), if the address doesn't contain it
        --replace-local         replace files you changed with the feed's version
        --jobs <n>              parallel downloads (default 4)
        --target <kind>=<folder>          where packs of another kind go, e.g. theme=~/.game/themes (repeatable);
        --extensions <kind>=.ini,.png     with the file extensions allowed there. Only from a signed feed whose
                                          key you pass with --key or in the address

Guides: docs/player-guide.md, docs/operator-guide.md, docs/signing.md, docs/hosting.md
`

// parse lets flags appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// version is shown by "kfeed version" and on the generated feed page; Run sets it.
var version = "dev"

// Run runs the kfeed command line. args are the full arguments including the program name (os.Args); ver is
// printed by "kfeed version". It returns the exit code.
func Run(args []string, ver string) int {
	version = ver
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	cmds := map[string]func([]string) error{
		"init": cmdInit, "keygen": cmdKeygen, "pubkey": cmdPubkey,
		"build": cmdBuild, "verify": cmdVerify, "serve": cmdServe, "sync": cmdSync,
		"version": func([]string) error { fmt.Printf("kfeed %s (format %s)\n", version, Spec); return nil },
	}
	cmd, ok := cmds[args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usageText)
		if args[1] == "help" || args[1] == "-h" || args[1] == "--help" {
			return 0
		}
		return 2
	}
	if err := cmd(args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
