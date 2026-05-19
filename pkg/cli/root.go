package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// Version is overridden at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:     "ccswap",
	Short:   "Claude Code account switcher",
	Version: Version,
}

// Execute runs the root command. On error it exits with code 1.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
