package cmd

import (
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "btunnel",
	Short: "Secure P2P Tunneling Engine for Docker & Web Environments",
	Long: `btunnel - Secure P2P Tunneling Engine for Docker & Web Environments.
Developed by @barronDEV (https://github.com/barronDEV)

btunnel is a zero-configuration networking utility that allows you to bypass
CGNAT and share local Docker networks or web applications directly using
end-to-end encrypted Peer-to-Peer (P2P) connections without traditional
port forwarding.

Two operational modes:
  • Mesh Mode (CLI-to-CLI): Raw TCP/UDP tunneling for gaming, databases, etc.
  • Web Mode (CLI-to-Browser): Zero-install browser access via Service Worker proxy.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Configure zerolog based on verbose flag
		zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
		if verbose {
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
			log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
			log.Debug().Msg("Verbose logging enabled")
		} else {
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
			log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
		}
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging for network debugging")
}

// Execute runs the root command.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
