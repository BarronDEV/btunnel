package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Version information — set at build time via ldflags
var (
	Version   = "0.1.0-dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information of btunnel",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("btunnel %s\n", Version)
		fmt.Printf("  Git Commit : %s\n", GitCommit)
		fmt.Printf("  Build Date : %s\n", BuildDate)
		fmt.Printf("  Go Version : %s\n", runtime.Version())
		fmt.Printf("  OS/Arch    : %s/%s\n", runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
