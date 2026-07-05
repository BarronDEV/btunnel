package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show statistics and health metrics of active tunnels",
	Long: `Show statistics and health metrics of active tunnels.

Displays real-time information about:
  • Active tunnel connections
  • Data transfer rates (upload/download)
  • Round-trip time (RTT) latency
  • Packet loss statistics
  • Connection uptime`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO: Implement status logic
		// 1. Read active tunnel state from local state file/socket
		// 2. Query WebRTC stats API via pion for RTT, bandwidth, packet loss
		// 3. Display formatted output (or TUI dashboard in Faz 8)

		fmt.Println("📊 btunnel Status")
		fmt.Println("─────────────────────────────────")
		fmt.Println("  No active tunnels.")
		fmt.Println()
		fmt.Println("  Use 'btunnel share' to start sharing,")
		fmt.Println("  or 'btunnel join' to connect to a peer.")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
