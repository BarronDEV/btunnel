package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/barronDEV/btunnel/internal/proxy"
	"github.com/barronDEV/btunnel/internal/signaling"
	"github.com/barronDEV/btunnel/internal/state"
	"github.com/barronDEV/btunnel/internal/ui"
	"github.com/barronDEV/btunnel/internal/util"
	btwebrtc "github.com/barronDEV/btunnel/internal/webrtc"
	"github.com/barronDEV/btunnel/signaling-server/handlers"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	joinLocalPort int
	joinNetName   string
	joinSignal    string
)

var joinCmd = &cobra.Command{
	Use:   "join [token]",
	Short: "Connect to a shared secure tunnel network exposed by another peer",
	Long: `Connect to an active P2P mesh network exposed by another peer.

This command establishes a direct UDP hole-punched connection to the host peer
and bridges your local Docker daemon or local ports to their isolated network.

The token format includes the host's signaling address automatically:
  bt-<token>@<host-ip>:<port>

No separate signaling server is needed — just paste the token from the host.`,
	Example: `  btunnel join bt-a7f3x9k2m1p4q8w5@192.168.1.50:9090 --local-port 25565
  btunnel join bt-b2c4d6e8f0g1h3j5@10.0.0.5:9090 --local-port 8080`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fullToken := args[0]

		if joinLocalPort == 0 {
			return fmt.Errorf("please specify a local port to bind using -l or --local-port")
		}

		connManager := state.NewConnectionManager()

		// --- Parse the enhanced token format ---
		// Supports: "bt-token@ip:port" (new) and "bt-token" (legacy)
		baseToken, sigAddr, hasEmbeddedAddr := util.ParseToken(fullToken)

		sigURL := joinSignal
		if sigURL == "" {
			sigURL = os.Getenv("BTUNNEL_SIGNAL_ADDR")
		}

		if sigURL == "" {
			if hasEmbeddedAddr {
				// Extract signaling URL from the token itself
				sigURL = fmt.Sprintf("ws://%s/ws", sigAddr)
				log.Info().
					Str("signaling_addr", sigAddr).
					Msg("Using signaling address from token")
			} else {
				// Fallback to the public signaling server
				sigURL = signaling.DefaultSignalingURL
				log.Info().
					Str("signaling_url", sigURL).
					Msg("No signaling address found in token. Using default public signaling server.")
			}
		}

		log.Info().
			Str("token", baseToken).
			Int("local_port", joinLocalPort).
			Str("signaling_url", sigURL).
			Msg("Joining tunnel")

		sigClient := signaling.NewClient(sigURL)

		spinnerModel := ui.DefaultConnectionSpinner()
		p := tea.NewProgram(spinnerModel)

		go func() {
			// Step 1: Connect to signaling server
			if err := sigClient.ConnectWithRetry(5); err != nil {
				p.Send(ui.StepErrorMsg{Err: fmt.Errorf("signaling connection failed: %w", err)})
				return
			}
			p.Send(ui.StepCompleteMsg{}) // Step 1 complete

			// Step 2: Join session (use base token without address part)
			sessionMsg, err := sigClient.JoinSession(baseToken)
			if err != nil {
				p.Send(ui.StepErrorMsg{Err: fmt.Errorf("failed to join session: %w", err)})
				return
			}

			var payload handlers.SessionCreatedPayload
			if err := json.Unmarshal(sessionMsg.Payload, &payload); err != nil {
				p.Send(ui.StepErrorMsg{Err: fmt.Errorf("failed to parse session payload: %w", err)})
				return
			}
			p.Send(ui.StepCompleteMsg{}) // Step 2 complete

			// Initialize connection info state
			tunnelInfo := &state.TunnelInfo{
				SessionID: payload.SessionID,
				Token:     baseToken,
				Mode:      "mesh",
				Target:    fmt.Sprintf("localhost:%d", joinLocalPort),
				State:     state.StateConnecting,
			}
			connManager.AddTunnel(tunnelInfo)

			// Step 3: Set up WebRTC Peer Connection
			iceServers := make([]webrtc.ICEServer, len(payload.ICEServers))
			for i, s := range payload.ICEServers {
				iceServers[i] = webrtc.ICEServer{
					URLs:       s.URLs,
					Username:   s.Username,
					Credential: s.Credential,
				}
			}

			peerCfg := btwebrtc.PeerConfig{
				ICEServers: iceServers,
				IsHost:     false,
				OnICECandidate: func(candidate *webrtc.ICECandidate) {
					if candidate != nil {
						candidateInit := candidate.ToJSON()
						var sdpMLineIndex *int
						if candidateInit.SDPMLineIndex != nil {
							val := int(*candidateInit.SDPMLineIndex)
							sdpMLineIndex = &val
						}
						sdpMid := ""
						if candidateInit.SDPMid != nil {
							sdpMid = *candidateInit.SDPMid
						}
						sigClient.SendICECandidate(payload.SessionID, "", candidateInit.Candidate, sdpMid, sdpMLineIndex)
					}
				},
				OnConnectionStateChange: func(s webrtc.PeerConnectionState) {
					switch s {
					case webrtc.PeerConnectionStateConnected:
						connManager.SetState(payload.SessionID, state.StateConnected)
					case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
						connManager.SetState(payload.SessionID, state.StateDisconnected)
					}
				},
			}

			peer, err := btwebrtc.NewPeer(peerCfg)
			if err != nil {
				p.Send(ui.StepErrorMsg{Err: fmt.Errorf("failed to init WebRTC: %w", err)})
				return
			}
			defer peer.Close()

			// Exchanging SDP negotiation
			sigClient.OnMessage(signaling.MsgTypeOffer, func(msg *signaling.SignalMessage) {
				var sdpPayload handlers.SDPPayload
				if err := json.Unmarshal(msg.Payload, &sdpPayload); err == nil {
					err = peer.SetRemoteDescription(webrtc.SessionDescription{
						Type: webrtc.NewSDPType(sdpPayload.Type),
						SDP:  sdpPayload.SDP,
					})
					if err != nil {
						log.Error().Err(err).Msg("Failed to set remote description")
						return
					}

					answer, err := peer.CreateAnswer()
					if err != nil {
						log.Error().Err(err).Msg("Failed to create answer")
						return
					}

					err = sigClient.SendSDP(payload.SessionID, "", "answer", answer.SDP)
					if err != nil {
						log.Error().Err(err).Msg("Failed to send SDP answer")
						return
					}
					p.Send(ui.StepCompleteMsg{}) // Step 3 complete
				}
			})

			sigClient.OnMessage(signaling.MsgTypeICECandidate, func(msg *signaling.SignalMessage) {
				var icePayload handlers.ICECandidatePayload
				if err := json.Unmarshal(msg.Payload, &icePayload); err == nil {
					var sdpMLineIndex *uint16
					if icePayload.SDPMLineIndex != nil {
						val := uint16(*icePayload.SDPMLineIndex)
						sdpMLineIndex = &val
					}
					peer.AddICECandidate(webrtc.ICECandidateInit{
						Candidate:     icePayload.Candidate,
						SDPMid:        &icePayload.SDPMid,
						SDPMLineIndex: sdpMLineIndex,
					})
				}
			})

			p.Send(ui.StepCompleteMsg{}) // Step 4 complete (negotiating data channels)

			// Wait for peer to connect
			for !peer.IsConnected() {
				time.Sleep(100 * time.Millisecond)
			}
			p.Send(ui.StepCompleteMsg{}) // Step 5 complete (connected)
			p.Send(ui.AllDoneMsg{})

			// Setup Client TCP Proxy
			dcManager := btwebrtc.NewDataChannelManager(peer)
			tcpProxy := proxy.NewTCPProxy(dcManager, fmt.Sprintf("127.0.0.1:%d", joinLocalPort))
			
			// Listen locally
			go func() {
				listenAddr := fmt.Sprintf(":%d", joinLocalPort)
				if err := tcpProxy.StartClient(listenAddr); err != nil {
					log.Error().Err(err).Msg("Proxy client failed to start")
				}
			}()
			defer tcpProxy.Stop()

			// Keep state stats updated
			go func() {
				for peer.IsConnected() {
					time.Sleep(1 * time.Second)
					statsReport := peer.GetStats()
					var rtt float64
					var bytesSent, bytesReceived uint64
					if statsData, err := json.Marshal(statsReport); err == nil {
						var rawStats map[string]map[string]interface{}
						if err := json.Unmarshal(statsData, &rawStats); err == nil {
							for _, stats := range rawStats {
								if t, ok := stats["type"].(string); ok {
									if t == "candidate-pair" {
										if rttVal, ok := stats["currentRoundTripTime"].(float64); ok {
											rtt = rttVal * 1000
										}
									}
									if t == "data-channel" {
										if sent, ok := stats["bytesSent"].(float64); ok {
											bytesSent += uint64(sent)
										}
										if recv, ok := stats["bytesReceived"].(float64); ok {
											bytesReceived += uint64(recv)
										}
									}
								}
							}
						}
					}
					connManager.UpdateStats(payload.SessionID, state.TunnelStats{
						RTT:           rtt,
						BytesSent:     bytesSent,
						BytesReceived: bytesReceived,
					})
				}
			}()

			<-cmd.Context().Done()
		}()

		if _, err := p.Run(); err != nil {
			return err
		}

		tunnels := connManager.GetAllTunnels()
		if len(tunnels) > 0 {
			dash := ui.NewDashboardModel(connManager, sigURL)
			if _, err := tea.NewProgram(dash).Run(); err != nil {
				log.Error().Err(err).Msg("Dashboard error")
			}
		}

		return nil
	},
}

func init() {
	joinCmd.Flags().IntVarP(&joinLocalPort, "local-port", "l", 0, "forward the remote network target to a specific local port")
	joinCmd.Flags().StringVarP(&joinNetName, "net-name", "n", "", "create a specific named virtual Docker network for this session")
	joinCmd.Flags().StringVarP(&joinSignal, "signal", "s", "", "specify custom signaling server URL (overrides token address)")

	rootCmd.AddCommand(joinCmd)
}
