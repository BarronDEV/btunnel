package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/barronDEV/btunnel/internal/config"
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
	runConfigPath string
	runLocal      bool
	runSignalPort int
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run multiple secure tunnels concurrently using a configuration file",
	Long: `Run multiple secure tunnels concurrently using a configuration file.
By default, it looks for 'btunnel.json' in the current working directory.

This allows you to expose multiple ports (e.g. web, ssh, game servers) under a single process.`,
	Example: `  btunnel run
  btunnel run --config my-tunnels.json
  btunnel run --local`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Use default path if none specified
		configPath := runConfigPath
		if configPath == "" {
			configPath = "btunnel.json"
		}

		log.Info().Str("path", configPath).Msg("Loading configuration file")
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Initialize state connection manager
		connManager := state.NewConnectionManager()

		// Context for coordinating goroutine lifecycles
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		// Read signaling address from config, environment, or use default public server
		sigURL := cfg.SignalURL
		if sigURL == "" {
			sigURL = os.Getenv("BTUNNEL_SIGNAL_ADDR")
		}

		var embeddedSrv *signaling.EmbeddedServer

		// Start embedded signaling server if --local flag is provided OR if no other server is specified and DefaultSignalingURL is empty
		if runLocal || (sigURL == "" && signaling.DefaultSignalingURL == "") {
			port := runSignalPort
			if port == 0 {
				port = 9090
			}

			// Find an available port (tries port, port+1, ..., port+9)
			availablePort, err := signaling.FindAvailablePort(port)
			if err != nil {
				return fmt.Errorf("could not find available port for signaling server: %w", err)
			}

			srv, err := signaling.StartEmbeddedServer(ctx, availablePort)
			if err != nil {
				return fmt.Errorf("failed to start embedded signaling server: %w", err)
			}
			embeddedSrv = srv

			sigURL = srv.WebSocketURL()
			log.Info().Int("port", availablePort).Msg("Embedded signaling server started automatically")
		} else if sigURL == "" {
			sigURL = signaling.DefaultSignalingURL
		}

		log.Info().Str("signaling_url", sigURL).Msg("Using signaling server")

		// We will print the share credentials before entering the dashboard TUI
		fmt.Printf("\n🌀  Starting %d tunnels...\n", len(cfg.Tunnels))
		fmt.Println("──────────────────────────────────────────────────")

		var wg sync.WaitGroup
		var startMutex sync.Mutex
		var initErrors []error

		// Start each tunnel concurrently
		for name, t := range cfg.Tunnels {
			wg.Add(1)
			go func(tunnelName string, tCfg config.TunnelConfig) {
				defer wg.Done()

				// Step 1: Create a dedicated signaling client
				sigClient := signaling.NewClient(sigURL)
				if err := sigClient.ConnectWithRetry(5); err != nil {
					startMutex.Lock()
					initErrors = append(initErrors, fmt.Errorf("tunnel '%s' signaling failed: %w", tunnelName, err))
					startMutex.Unlock()
					return
				}

				// Step 2: Create a session on the server
				sessionMsg, err := sigClient.CreateSession(tCfg.Type, tCfg.Target)
				if err != nil {
					startMutex.Lock()
					initErrors = append(initErrors, fmt.Errorf("tunnel '%s' failed to create session: %w", tunnelName, err))
					startMutex.Unlock()
					return
				}

				var payload handlers.SessionCreatedPayload
				if err := json.Unmarshal(sessionMsg.Payload, &payload); err != nil {
					startMutex.Lock()
					initErrors = append(initErrors, fmt.Errorf("tunnel '%s' failed to parse payload: %w", tunnelName, err))
					startMutex.Unlock()
					return
				}

				// Format enhanced token if local embedded server is active
				displayToken := payload.Token
				if embeddedSrv != nil {
					localIP := util.GetLocalIP()
					displayToken = util.FormatToken(payload.Token, localIP, embeddedSrv.Port())
				}

				// Print info safely
				startMutex.Lock()
				fmt.Printf("📦  Tunnel [%s]\n", tunnelName)
				fmt.Printf("    Mode:  %s\n", tCfg.Type)
				fmt.Printf("    Token: %s\n", displayToken)
				if tCfg.Type == "web" {
					var webURL string
					if embeddedSrv != nil {
						localIP := util.GetLocalIP()
						webURL = embeddedSrv.WebURL(localIP)
					} else {
						webURL = fmt.Sprintf("https://handshake.btunnel.dpdns.org:8443/share/#%s", payload.Token)
					}
					fmt.Printf("    URL:   %s\n", webURL)
				}
				fmt.Println("──────────────────────────────────────────────────")
				startMutex.Unlock()

				// Add tunnel to tracking manager
				tunnelInfo := &state.TunnelInfo{
					SessionID: payload.SessionID,
					Token:     displayToken,
					Mode:      tCfg.Type,
					Target:    tCfg.Target,
					State:     state.StateConnecting,
				}
				connManager.AddTunnel(tunnelInfo)

				// Step 3: Set up WebRTC Peer Connection Config
				iceServers := make([]webrtc.ICEServer, len(payload.ICEServers))
				for i, s := range payload.ICEServers {
					iceServers[i] = webrtc.ICEServer{
						URLs:       s.URLs,
						Username:   s.Username,
						Credential: s.Credential,
					}
				}

				var activePeers sync.Map   // clientID -> *btwebrtc.Peer
				var activeProxies sync.Map // clientID -> func()

				// Goroutine lifecycle management (Clean up all peers on exit)
				go func() {
					<-ctx.Done()
					activeProxies.Range(func(key, value interface{}) bool {
						value.(func())()
						return true
					})
					activePeers.Range(func(key, value interface{}) bool {
						value.(*btwebrtc.Peer).Close()
						return true
					})
					sigClient.Close()
				}()

				// Signaling handlers
				sigClient.OnMessage(signaling.MsgTypeAnswer, func(msg *signaling.SignalMessage) {
					var sdpPayload handlers.SDPPayload
					if err := json.Unmarshal(msg.Payload, &sdpPayload); err == nil {
						if peerVal, ok := activePeers.Load(msg.ClientID); ok {
							peer := peerVal.(*btwebrtc.Peer)
							peer.SetRemoteDescription(webrtc.SessionDescription{
								Type: webrtc.NewSDPType(sdpPayload.Type),
								SDP:  sdpPayload.SDP,
							})
						}
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
						if peerVal, ok := activePeers.Load(msg.ClientID); ok {
							peer := peerVal.(*btwebrtc.Peer)
							peer.AddICECandidate(webrtc.ICECandidateInit{
								Candidate:     icePayload.Candidate,
								SDPMid:        &icePayload.SDPMid,
								SDPMLineIndex: sdpMLineIndex,
							})
						}
					}
				})

				sigClient.OnMessage(signaling.MsgTypePeerJoined, func(msg *signaling.SignalMessage) {
					clientID := msg.ClientID
					if clientID == "" {
						log.Warn().Msg("Peer joined message received without ClientID")
						return
					}
					log.Info().Str("tunnel", tunnelName).Str("client_id", clientID).Msg("Peer joined. Setting up WebRTC...")

					// Clean up previous connection for this specific client if it exists (e.g. on page refresh)
					if oldProxyVal, ok := activeProxies.Load(clientID); ok {
						oldProxyVal.(func())()
						activeProxies.Delete(clientID)
					}
					if oldPeerVal, ok := activePeers.Load(clientID); ok {
						oldPeerVal.(*btwebrtc.Peer).Close()
						activePeers.Delete(clientID)
					}

					// Setup Peer Connection Config specifically for this clientID
					peerCfg := btwebrtc.PeerConfig{
						ICEServers: iceServers,
						IsHost:     true,
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
								sigClient.SendICECandidate(payload.SessionID, clientID, candidateInit.Candidate, sdpMid, sdpMLineIndex)
							}
						},
						OnConnectionStateChange: func(s webrtc.PeerConnectionState) {
							switch s {
							case webrtc.PeerConnectionStateConnected:
								connManager.SetState(payload.SessionID, state.StateConnected)
							case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
								// Only set disconnected state if ALL clients are disconnected
								allDisconnected := true
								activePeers.Range(func(key, value interface{}) bool {
									if value.(*btwebrtc.Peer).IsConnected() {
										allDisconnected = false
										return false
									}
									return true
								})
								if allDisconnected {
									connManager.SetState(payload.SessionID, state.StateDisconnected)
								}
							}
						},
					}

					peer, err := btwebrtc.NewPeer(peerCfg)
					if err != nil {
						log.Error().Err(err).Str("tunnel", tunnelName).Str("client_id", clientID).Msg("Failed to create WebRTC peer")
						return
					}
					activePeers.Store(clientID, peer)

					if err := peer.CreateDataChannels(btwebrtc.DefaultChannelCount); err != nil {
						log.Error().Err(err).Str("tunnel", tunnelName).Str("client_id", clientID).Msg("Failed to create data channels")
						peer.Close()
						activePeers.Delete(clientID)
						return
					}

					dcManager := btwebrtc.NewDataChannelManager(peer)
					targetAddr := tCfg.Target
					if !strings.Contains(targetAddr, ":") {
						targetAddr = fmt.Sprintf("127.0.0.1:%s", targetAddr)
					}

					var stopProxy func()
					if tCfg.Type == "web" {
						httpProxy := proxy.NewHTTPProxy(dcManager, formatURL(targetAddr))
						go httpProxy.StartHost()
						stopProxy = func() {
							httpProxy.Stop()
						}
					} else {
						tcpProxy := proxy.NewTCPProxy(dcManager, targetAddr)
						go tcpProxy.StartHost()
						stopProxy = func() {
							tcpProxy.Stop()
						}
					}
					activeProxies.Store(clientID, stopProxy)

					offer, err := peer.CreateOffer()
					if err != nil {
						log.Error().Err(err).Str("tunnel", tunnelName).Str("client_id", clientID).Msg("Failed to create offer")
						return
					}
					if err := sigClient.SendSDP(payload.SessionID, clientID, "offer", offer.SDP); err != nil {
						log.Error().Err(err).Str("tunnel", tunnelName).Str("client_id", clientID).Msg("Failed to send offer")
						return
					}
				})

				// Real-time stats update thread (aggregates stats from all connected clients)
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						case <-time.After(1 * time.Second):
							var totalRTT float64
							var connectedCount float64
							var totalSent, totalReceived uint64

							activePeers.Range(func(key, value interface{}) bool {
								peer := value.(*btwebrtc.Peer)
								if !peer.IsConnected() {
									return true
								}
								connectedCount++
								statsReport := peer.GetStats()
								var peerRTT float64
								var peerSent, peerReceived uint64
								if statsData, err := json.Marshal(statsReport); err == nil {
									var rawStats map[string]map[string]interface{}
									if err := json.Unmarshal(statsData, &rawStats); err == nil {
										for _, stats := range rawStats {
											if t, ok := stats["type"].(string); ok {
												if t == "candidate-pair" {
													if rttVal, ok := stats["currentRoundTripTime"].(float64); ok {
														peerRTT = rttVal * 1000
													}
												}
												if t == "data-channel" {
													if sent, ok := stats["bytesSent"].(float64); ok {
														peerSent += uint64(sent)
													}
													if recv, ok := stats["bytesReceived"].(float64); ok {
														peerReceived += uint64(recv)
													}
												}
											}
										}
									}
								}
								totalRTT += peerRTT
								totalSent += peerSent
								totalReceived += peerReceived
								return true
							})

							avgRTT := 0.0
							if connectedCount > 0 {
								avgRTT = totalRTT / connectedCount
							}

							connManager.UpdateStats(payload.SessionID, state.TunnelStats{
								RTT:           avgRTT,
								BytesSent:     totalSent,
								BytesReceived: totalReceived,
							})
						}
					}
				}()
			}(name, t)
		}

		// Wait briefly for sessions to initialize or throw error
		wg.Wait()

		if len(initErrors) > 0 {
			for _, err := range initErrors {
				log.Error().Err(err).Msg("Tunnel initialization failed")
			}
			return fmt.Errorf("failed to start one or more tunnels")
		}

		// Wait 2 seconds so the user can read the console tokens before entering TUI dashboard
		time.Sleep(2 * time.Second)

		// Start Bubbletea live status dashboard
		dash := ui.NewDashboardModel(connManager, sigURL)
		if _, err := tea.NewProgram(dash).Run(); err != nil {
			log.Error().Err(err).Msg("Dashboard run failed")
			return err
		}

		return nil
	},
}

func init() {
	runCmd.Flags().StringVarP(&runConfigPath, "config", "c", "", "path to JSON configuration file (default is 'btunnel.json')")
	runCmd.Flags().BoolVarP(&runLocal, "local", "L", false, "start a local embedded signaling server instead of the public one")
	runCmd.Flags().IntVar(&runSignalPort, "signal-port", 9090, "port for the embedded signaling server (default 9090)")
	rootCmd.AddCommand(runCmd)
}

func formatURL(addr string) string {
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return "http://" + addr
	}
	return addr
}
