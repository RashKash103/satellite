// sat: a Satellite node in one binary. See docs/node.md.
package main

import (
	"os"

	"github.com/RashKash103/satellite/internal/cli"
)

var version = "dev" // set by the release build: -ldflags "-X main.version=vX.Y.Z"

func main() { os.Exit(cli.Main(version, os.Args)) }
