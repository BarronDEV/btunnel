package proxy

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	btwebrtc "github.com/barronDEV/btunnel/internal/webrtc"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// HTTPProxy handles HTTP reverse proxy over WebRTC DataChannel.
// Used for Web Mode (CLI-to-Browser) to proxy browser HTTP requests
// to local Docker containers or services.
type HTTPProxy struct {
	dcManager *btwebrtc.DataChannelManager
	targetURL string // e.g., "http://localhost:8080"

	// Request ID counter
	requestIDCounter atomic.Uint32

	// Pending responses: requestID -> response channel
	pendingResponses sync.Map

	// Active virtual WebSocket connections
	wsConns sync.Map

	done chan struct{}
}

// HTTPRequest is the serialized HTTP request sent over DataChannel.
type HTTPRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64 encoded for binary data
}

// HTTPResponse is the serialized HTTP response sent back over DataChannel.
type HTTPResponse struct {
	ID         string            `json:"id"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"` // base64 encoded
}

// NewHTTPProxy creates a new HTTP proxy.
func NewHTTPProxy(dcManager *btwebrtc.DataChannelManager, targetURL string) *HTTPProxy {
	return &HTTPProxy{
		dcManager: dcManager,
		targetURL: targetURL,
		done:      make(chan struct{}),
	}
}

// StartHost runs the host-side HTTP proxy that forwards DataChannel requests to local HTTP servers.
func (p *HTTPProxy) StartHost() error {
	log.Info().Str("target", p.targetURL).Msg("Starting HTTP proxy (host mode)")

	messages := make(chan *btwebrtc.ReassembledMessage, 64)
	go p.dcManager.ProcessIncoming(messages)

	for {
		select {
		case <-p.done:
			return nil
		case msg, ok := <-messages:
			if !ok {
				return nil
			}
			go p.handleHostRequest(msg)
		}
	}
}

// handleHostRequest processes an HTTP or WebSocket virtual request from the browser client.
func (p *HTTPProxy) handleHostRequest(msg *btwebrtc.ReassembledMessage) {
	var req HTTPRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		log.Error().Err(err).Msg("Failed to parse HTTP request")
		return
	}

	// Intercept WebSocket virtual packets
	if strings.HasPrefix(req.Method, "WS_") {
		p.handleWebSocketRequest(msg.RequestID, &req)
		return
	}

	log.Debug().
		Str("id", req.ID).
		Str("method", req.Method).
		Str("url", req.URL).
		Msg("Proxying HTTP request")

	// Build the full target URL
	fullURL := p.targetURL + req.URL

	// Create HTTP request
	var bodyReader io.Reader
	if req.Body != "" {
		// If body is base64 encoded binary, decode it first
		if decoded, err := base64.StdEncoding.DecodeString(req.Body); err == nil {
			bodyReader = io.NopCloser(strings.NewReader(string(decoded)))
		} else {
			bodyReader = strings.NewReader(req.Body)
		}
	}

	httpReq, err := http.NewRequest(req.Method, fullURL, bodyReader)
	if err != nil {
		p.sendErrorResponse(msg.RequestID, req.ID, fmt.Sprintf("Failed to create request: %v", err))
		return
	}

	// Set headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// Execute HTTP request
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		p.sendErrorResponse(msg.RequestID, req.ID, fmt.Sprintf("Request failed: %v", err))
		return
	}
	defer httpResp.Body.Close()

	// Read response body
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		p.sendErrorResponse(msg.RequestID, req.ID, fmt.Sprintf("Failed to read response: %v", err))
		return
	}

	// Build response headers
	headers := make(map[string]string)
	for key, values := range httpResp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	// Base64 encode the response body
	encodedBody := base64.StdEncoding.EncodeToString(body)

	// Send response back via DataChannel
	response := HTTPResponse{
		ID:         req.ID,
		StatusCode: httpResp.StatusCode,
		Headers:    headers,
		Body:       encodedBody,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		log.Error().Err(err).Str("id", req.ID).Msg("Failed to marshal HTTP response")
		return
	}

	if err := p.dcManager.SendMessage(msg.RequestID, responseData); err != nil {
		log.Error().Err(err).Str("id", req.ID).Msg("Failed to send HTTP response via DataChannel")
	}

	log.Debug().
		Str("id", req.ID).
		Int("status", httpResp.StatusCode).
		Int("body_size", len(body)).
		Msg("HTTP response sent")
}

// handleWebSocketRequest processes virtual WS connection steps.
func (p *HTTPProxy) handleWebSocketRequest(requestID uint32, req *HTTPRequest) {
	switch req.Method {
	case "WS_CONNECT":
		log.Info().Str("socket_id", req.ID).Str("url", req.URL).Msg("Virtual WebSocket connecting")

		// Convert http:// target to ws:// target URL
		wsURL := req.URL
		if strings.HasPrefix(wsURL, "/") {
			// Relative path, prepend the base targetURL converted to ws
			baseWS := p.targetURL
			baseWS = strings.Replace(baseWS, "http://", "ws://", 1)
			baseWS = strings.Replace(baseWS, "https://", "wss://", 1)
			wsURL = baseWS + wsURL
		} else {
			wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
			wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
		}

		dialer := websocket.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			HandshakeTimeout: 10 * time.Second,
		}

		// Pull request headers
		headers := make(http.Header)
		for k, v := range req.Headers {
			headers.Set(k, v)
		}

		conn, _, err := dialer.Dial(wsURL, headers)
		if err != nil {
			log.Error().Err(err).Str("socket_id", req.ID).Msg("Failed to dial virtual WebSocket target")
			p.sendErrorResponse(requestID, req.ID, "WS_DIAL_FAILED: "+err.Error())
			return
		}

		p.wsConns.Store(req.ID, conn)

		// Send success handshake back
		resp := HTTPResponse{
			ID:         req.ID,
			StatusCode: 101, // Switching Protocols
			Headers:    map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"},
		}
		data, _ := json.Marshal(resp)
		_ = p.dcManager.SendMessage(requestID, data)

		// Start reader loop from local WS server
		go func(socketID string, wsConn *websocket.Conn) {
			defer func() {
				wsConn.Close()
				p.wsConns.Delete(socketID)
				
				// Send close event to browser client
				closeMsg := HTTPRequest{
					ID:      socketID,
					Method:  "WS_CLOSE",
					Headers: map[string]string{"X-Code": "1000", "X-Reason": "Normal Closure"},
				}
				respJSON, _ := json.Marshal(closeMsg)
				_ = p.dcManager.SendMessage(requestID, respJSON)
			}()

			for {
				msgType, message, err := wsConn.ReadMessage()
				if err != nil {
					break
				}

				isBinary := msgType == websocket.BinaryMessage
				var body string
				if isBinary {
					body = base64.StdEncoding.EncodeToString(message)
				} else {
					body = string(message)
				}

				pushResponse := struct {
					ID      string            `json:"id"`
					Method  string            `json:"method"`
					Body    string            `json:"body"`
					Headers map[string]string `json:"headers"`
				}{
					ID:     socketID,
					Method: "WS_DATA",
					Body:   body,
					Headers: map[string]string{
						"X-Binary": fmt.Sprintf("%t", isBinary),
					},
				}

				data, err := json.Marshal(pushResponse)
				if err == nil {
					_ = p.dcManager.SendMessage(requestID, data)
				}
			}
		}(req.ID, conn)

	case "WS_DATA":
		connVal, ok := p.wsConns.Load(req.ID)
		if !ok {
			return
		}
		conn := connVal.(*websocket.Conn)

		isBinary := req.Headers != nil && req.Headers["X-Binary"] == "true"
		var payload []byte
		var err error

		if isBinary {
			payload, err = base64.StdEncoding.DecodeString(req.Body)
			if err != nil {
				log.Error().Err(err).Str("socket_id", req.ID).Msg("Failed to decode base64 binary WS body")
				return
			}
		} else {
			payload = []byte(req.Body)
		}

		msgType := websocket.TextMessage
		if isBinary {
			msgType = websocket.BinaryMessage
		}

		_ = conn.WriteMessage(msgType, payload)

	case "WS_CLOSE":
		if connVal, ok := p.wsConns.Load(req.ID); ok {
			conn := connVal.(*websocket.Conn)
			conn.Close()
			p.wsConns.Delete(req.ID)
			log.Info().Str("socket_id", req.ID).Msg("Virtual WebSocket connection closed by client")
		}
	}
}

// sendErrorResponse sends an error HTTP response back to the client.
func (p *HTTPProxy) sendErrorResponse(requestID uint32, reqID string, errMsg string) {
	response := HTTPResponse{
		ID:         reqID,
		StatusCode: 502,
		Headers:    map[string]string{"Content-Type": "text/plain"},
		Body:       errMsg,
	}

	responseData, _ := json.Marshal(response)
	p.dcManager.SendMessage(requestID, responseData)

	log.Error().Str("id", reqID).Str("error", errMsg).Msg("HTTP proxy error")
}

// Stop gracefully stops the HTTP proxy and closes any active WS tunnels.
func (p *HTTPProxy) Stop() {
	close(p.done)
	p.wsConns.Range(func(key, value interface{}) bool {
		if conn, ok := value.(*websocket.Conn); ok {
			conn.Close()
		}
		return true
	})
	log.Info().Msg("HTTP proxy stopped")
}

// jsonStringReader creates a reader from a JSON-encoded string body.
func jsonStringReader(s string) io.Reader {
	return &stringReader{s: s, i: 0}
}

type stringReader struct {
	s string
	i int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
