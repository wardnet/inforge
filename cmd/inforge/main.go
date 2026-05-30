// Command inforge is the entrypoint for the inforge CLI.
//
// At this phase it only reports its version and exits — the full command
// surface is introduced in later PRs.
package main

import "fmt"

// version is the build version, overridden at release time via -ldflags
// "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

func main() {
	fmt.Println("inforge", version)
}
