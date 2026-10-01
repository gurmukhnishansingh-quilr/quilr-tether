// Command tether tethers AI coding agents (currently Claude Code) to the Quilr LLM Gateway.
package main

import (
	"os"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/app"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	app.Version = version
	os.Exit(app.Main(os.Args[1:]))
}
