package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/barronDEV/btunnel/internal/signaling"
	btwebrtc "github.com/barronDEV/btunnel/internal/webrtc"
	"github.com/barronDEV/btunnel/signaling-server/handlers"
	"github.com/barronDEV/btunnel/signaling-server/store"
	"github.com/pion/webrtc/v4"
)

func TestEndToEndTunnel(t *testing.T) {
	// 1. Start Mock HTTP Target Server
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Response", "from-target")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello-from-local-target-server"))
	}))
	defer targetServer.Close()

	// 2. Start Signaling Server in background
	sessionStore := store.NewMemoryStore()
	handler := handlers.NewHandler(sessionStore)
	sigMux := http.NewServeMux()
	sigMux.HandleFunc("/ws", handler.HandleWebSocket)
	sigServer := httptest.NewServer(sigMux)
	defer sigServer.Close()

	// Convert http:// to ws:// for signaling client
	sigURL := "ws" + strings.TrimPrefix(sigServer.URL, "http") + "/ws"

	// 3. Initiate Host Connection
	hostSigClient := signaling.NewClient(sigURL)
	if err := hostSigClient.Connect(); err != nil {
		t.Fatalf("Host failed to connect to signaling: %v", err)
	}
	defer hostSigClient.Close()

	sessionMsg, err := hostSigClient.CreateSession("mesh", targetServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("Host failed to create session: %v", err)
	}

	var payload handlers.SessionCreatedPayload
	if err := json.Unmarshal(sessionMsg.Payload, &payload); err != nil {
		t.Fatalf("Failed to parse session created: %v", err)
	}

	_ = payload.SessionID
	token := payload.Token

	// 4. Initiate Client Connection
	clientSigClient := signaling.NewClient(sigURL)
	if err := clientSigClient.Connect(); err != nil {
		t.Fatalf("Client failed to connect to signaling: %v", err)
	}
	defer clientSigClient.Close()

	clientSessionMsg, err := clientSigClient.JoinSession(token)
	if err != nil {
		t.Fatalf("Client failed to join session: %v", err)
	}

	var clientPayload handlers.SessionCreatedPayload
	if err := json.Unmarshal(clientSessionMsg.Payload, &clientPayload); err != nil {
		t.Fatalf("Failed to parse client session: %v", err)
	}

	// 5. Setup WebRTC Peer Connections
	// Convert ICE configs
	mapICEServers := func(src []handlers.ICEServer) []webrtc.ICEServer {
		out := make([]webrtc.ICEServer, len(src))
		for i, s := range src {
			out[i] = webrtc.ICEServer{
				URLs:       s.URLs,
				Username:   s.Username,
				Credential: s.Credential,
			}
		}
		return out
	}

	var hostPeer *btwebrtc.Peer
	var clientPeer *btwebrtc.Peer

	hostPeerCfg := btwebrtc.PeerConfig{
		ICEServers: mapICEServers(payload.ICEServers),
		IsHost:     true,
		OnICECandidate: func(candidate *webrtc.ICECandidate) {
			if candidate != nil && clientPeer != nil {
				clientPeer.AddICECandidate(candidate.ToJSON())
			}
		},
		OnConnectionStateChange: func(s webrtc.PeerConnectionState) {
			t.Logf("Host peer connection state: %s", s)
		},
	}
	hostPeer, err = btwebrtc.NewPeer(hostPeerCfg)
	if err != nil {
		t.Fatalf("Failed to create host peer: %v", err)
	}
	defer hostPeer.Close()

	clientPeerCfg := btwebrtc.PeerConfig{
		ICEServers: mapICEServers(clientPayload.ICEServers),
		IsHost:     false,
		OnICECandidate: func(candidate *webrtc.ICECandidate) {
			if candidate != nil && hostPeer != nil {
				hostPeer.AddICECandidate(candidate.ToJSON())
			}
		},
		OnConnectionStateChange: func(s webrtc.PeerConnectionState) {
			t.Logf("Client peer connection state: %s", s)
		},
	}
	clientPeer, err = btwebrtc.NewPeer(clientPeerCfg)
	if err != nil {
		t.Fatalf("Failed to create client peer: %v", err)
	}
	defer clientPeer.Close()

	// Create channels on host
	if err := hostPeer.CreateDataChannels(2); err != nil {
		t.Fatalf("Failed to create host channels: %v", err)
	}

	// 6. Perform SDP Handshake
	offer, err := hostPeer.CreateOffer()
	if err != nil {
		t.Fatalf("Failed to create offer: %v", err)
	}

	if err := clientPeer.SetRemoteDescription(offer); err != nil {
		t.Fatalf("Client failed to set remote description: %v", err)
	}

	answer, err := clientPeer.CreateAnswer()
	if err != nil {
		t.Fatalf("Client failed to create answer: %v", err)
	}

	if err := hostPeer.SetRemoteDescription(answer); err != nil {
		t.Fatalf("Host failed to set remote description: %v", err)
	}

	// 7. Exchange ICE Candidates
	// Candidates are trickled automatically via callbacks.

	// Wait for WebRTC connection
	connected := false
	for i := 0; i < 30; i++ {
		if hostPeer.IsConnected() && clientPeer.IsConnected() {
			connected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !connected {
		t.Fatal("WebRTC failed to connect within 3 seconds")
	}
	t.Log("WebRTC E2E P2P connection successfully established!")

	// Wait for data channels to transition to Open state
	dcOpen := false
	for i := 0; i < 50; i++ {
		hostOpen := false
		clientOpen := false
		for _, dc := range hostPeer.GetDataChannels() {
			if dc.ReadyState() == webrtc.DataChannelStateOpen {
				hostOpen = true
				break
			}
		}
		for _, dc := range clientPeer.GetDataChannels() {
			if dc.ReadyState() == webrtc.DataChannelStateOpen {
				clientOpen = true
				break
			}
		}
		if hostOpen && clientOpen {
			dcOpen = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !dcOpen {
		t.Fatal("Data channels failed to open within 5 seconds")
	}
	t.Log("WebRTC DataChannels successfully opened!")

	// 8. Start Proxy Engines
	hostDCManager := btwebrtc.NewDataChannelManager(hostPeer)
	hostProxy := NewTCPProxy(hostDCManager, targetServer.Listener.Addr().String())
	go hostProxy.StartHost()
	defer hostProxy.Stop()

	clientDCManager := btwebrtc.NewDataChannelManager(clientPeer)
	clientProxy := NewTCPProxy(clientDCManager, targetServer.Listener.Addr().String())
	
	// Bind client listener
	clientAddr := "127.0.0.1:28090"
	go func() {
		if err := clientProxy.StartClient(clientAddr); err != nil {
			t.Logf("Client proxy shut down: %v", err)
		}
	}()
	defer clientProxy.Stop()

	// Wait for client listener to boot up
	time.Sleep(200 * time.Millisecond)

	// 9. Send Test HTTP Request through the WebRTC Tunnel
	t.Log("Sending test request through client port...")
	resp, err := http.Get("http://" + clientAddr + "/")
	if err != nil {
		t.Fatalf("Failed to send request through tunnel: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	// 10. Verify response
	t.Logf("Response received through tunnel: %s", string(body))
	if string(body) != "hello-from-local-target-server" {
		t.Errorf("Expected 'hello-from-local-target-server', got '%s'", string(body))
	}

	if resp.Header.Get("X-Test-Response") != "from-target" {
		t.Errorf("Response header missing or incorrect")
	}

	t.Log("End-to-End P2P Tunnel test passed successfully!")
}
