// kfeed builds, signs, checks and serves karaoke feeds (SPEC.md, karaoke-feed/0.2).
// Standard library only.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usageText = `kfeed - build, sign, check and serve karaoke feeds (spec karaoke-feed/0.2)

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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmds := map[string]func([]string) error{
		"init": cmdInit, "keygen": cmdKeygen, "pubkey": cmdPubkey,
		"build": cmdBuild, "verify": cmdVerify, "serve": cmdServe, "sync": cmdSync,
		"version": func([]string) error { fmt.Printf("kfeed %s (format %s)\n", version, Spec); return nil },
	}
	cmd, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usageText)
		if os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
			return
		}
		os.Exit(2)
	}
	if err := cmd(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
