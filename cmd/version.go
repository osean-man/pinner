package cmd

import "github.com/osean-man/pinner/internal/buildinfo"

// SetBuildInfo sets what `pinner --version` reports.
func SetBuildInfo(version, commit, date string) {
	rootCmd.Version = buildinfo.Resolve(version, commit, date).String()
	rootCmd.SetVersionTemplate("pinner {{.Version}}\n")
}
