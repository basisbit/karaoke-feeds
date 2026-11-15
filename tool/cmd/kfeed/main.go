// Command kfeed builds, signs, checks and serves karaoke feeds, and downloads them into a library folder.
package main

import (
	"os"

	kfeed "github.com/basisbit/karaoke-feeds/tool"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(kfeed.Run(os.Args, version))
}
