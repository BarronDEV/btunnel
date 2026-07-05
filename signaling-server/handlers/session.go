package handlers

import (
	"encoding/json"
	"time"
)

// MessageType represents the type of signaling message.
type MessageType string

const (
	// MsgTypeOffer is an SDP offer from host to client.
	MsgTypeOffer MessageType = "offer"

	// MsgTypeAnswer is an SDP answer from client to host.
	MsgTypeAnswer MessageType = "answer"

	// MsgTypeICECandidate is an ICE candidate exchange.
	MsgTypeICECandidate MessageType = "ice-candidate"

	// MsgTypeJoin is a client requesting to join a session.
	MsgTypeJoin MessageType = "join"

	// MsgTypeCreate is a host requesting to create a session.
	MsgTypeCreate MessageType = "create"

	// MsgTypeSessionCreated confirms session creation with token.
	MsgTypeSessionCreated MessageType = "session-created"

	// MsgTypeError signals an error condition.
	MsgTypeError MessageType = "error"

	// MsgTypePeerJoined notifies host that a client has joined.
	MsgTypePeerJoined MessageType = "peer-joined"

	// MsgTypePeerLeft notifies that a peer has disconnected.
	MsgTypePeerLeft MessageType = "peer-left"
)

// SignalMessage is the JSON protocol for signaling communication.
type SignalMessage struct {
	Type      MessageType     `json:"type"`
	SessionID string          `json:"sessionid,omitempty"`
	ClientID  string          `json:"clientid,omitempty"`
	Token     string          `json:"token,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Nonce     string          `json:"nonce,omitempty"`
}

// SDPPayload carries SDP offer/answer data.
type SDPPayload struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"` // "offer" or "answer"
}

// ICECandidatePayload carries an ICE candidate.
type ICECandidatePayload struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex *int   `json:"sdpMLineIndex,omitempty"`
}

// CreatePayload carries session creation parameters.
type CreatePayload struct {
	Mode   string `json:"mode"`   // "mesh" or "web"
	Target string `json:"target"` // Docker network or port
}

// ICEServer represents a STUN/TURN server configuration returned to peers.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// SessionCreatedPayload carries the response after session creation.
type SessionCreatedPayload struct {
	Token      string      `json:"token"`
	SessionID  string      `json:"sessionid"`
	ICEServers []ICEServer `json:"ice_servers"`
}

// ErrorPayload carries error details.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewSignalMessage creates a new SignalMessage with current timestamp.
func NewSignalMessage(msgType MessageType, sessionID string, payload interface{}) (*SignalMessage, error) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rawPayload = data
	}

	return &SignalMessage{
		Type:      msgType,
		SessionID: sessionID,
		Payload:   rawPayload,
		Timestamp: time.Now().Unix(),
	}, nil
}

// NewErrorMessage creates an error signal message.
func NewErrorMessage(sessionID, code, message string) (*SignalMessage, error) {
	return NewSignalMessage(MsgTypeError, sessionID, ErrorPayload{
		Code:    code,
		Message: message,
	})
}
