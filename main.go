package main

import "github.com/osean-man/pinner/cmd"

// Set by release builds with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version string
	commit  string
	date    string
)

func main() {
	cmd.SetBuildInfo(version, commit, date)
	cmd.Execute()
}
