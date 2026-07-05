package webrtc

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// MaxChunkSize is the maximum size per SCTP message (16KB safe limit).
	MaxChunkSize = 16 * 1024

	// DefaultChannelCount is the default number of parallel data channels.
	// Multiple channels prevent head-of-line blocking for concurrent requests.
	DefaultChannelCount = 4

	// HeaderSize is the byte size of our framing header.
	// Format: [4 bytes requestID][4 bytes chunkIndex][4 bytes totalChunks][4 bytes payloadLen]
	HeaderSize = 16
)

// DataChannelManager handles multiplexed data transmission across multiple channels.
type DataChannelManager struct {
	peer *Peer

	// Round-robin counter for channel selection
	rrCounter atomic.Uint64

	// Reassembly buffer for chunked messages
	reassembly sync.Map // requestID -> *reassemblyBuffer
}

// reassemblyBuffer holds chunks for a multi-chunk message.
type reassemblyBuffer struct {
	mu          sync.Mutex
	totalChunks uint32
	received    map[uint32][]byte
}

// Frame represents a data frame sent over DataChannel.
type Frame struct {
	// RequestID uniquely identifies a request/response pair.
	RequestID uint32

	// ChunkIndex is the index of this chunk (0-based).
	ChunkIndex uint32

	// TotalChunks is the total number of chunks for this message.
	TotalChunks uint32

	// Payload is the actual data.
	Payload []byte
}

// NewDataChannelManager creates a new manager for multiplexed data channels.
func NewDataChannelManager(peer *Peer) *DataChannelManager {
	mgr := &DataChannelManager{
		peer: peer,
	}
	go mgr.startHeartbeatLoop()
	return mgr
}

// startHeartbeatLoop sends a heartbeat ping frame every 10 seconds to keep UDP and SCTP bindings alive.
func (m *DataChannelManager) startHeartbeatLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !m.peer.IsConnected() {
				// Wait for connection
				continue
			}
			// Send ping frame (RequestID: 0, ChunkIndex: 0, TotalChunks: 1, Payload: "ping")
			pingFrame := Frame{
				RequestID:   0,
				ChunkIndex:  0,
				TotalChunks: 1,
				Payload:     []byte(`{"type":"ping"}`),
			}
			encoded := encodeFrame(&pingFrame)
			// Ignore error as channel might be temporarily full or closed
			_ = m.peer.Send(encoded)
		}
	}
}

// SendMessage sends a message, automatically chunking if it exceeds MaxChunkSize.
// Uses round-robin channel selection for load distribution.
func (m *DataChannelManager) SendMessage(requestID uint32, data []byte) error {
	maxPayload := MaxChunkSize - HeaderSize
	totalChunks := uint32((len(data) + maxPayload - 1) / maxPayload)

	for i := uint32(0); i < totalChunks; i++ {
		start := int(i) * maxPayload
		end := start + maxPayload
		if end > len(data) {
			end = len(data)
		}

		frame := Frame{
			RequestID:   requestID,
			ChunkIndex:  i,
			TotalChunks: totalChunks,
			Payload:     data[start:end],
		}

		encoded := encodeFrame(&frame)
		if err := m.peer.Send(encoded); err != nil {
			return fmt.Errorf("failed to send chunk %d/%d for request %d: %w",
				i+1, totalChunks, requestID, err)
		}

		log.Debug().
			Uint32("request_id", requestID).
			Uint32("chunk", i+1).
			Uint32("total", totalChunks).
			Int("payload_size", len(frame.Payload)).
			Msg("Sent data chunk")
	}

	return nil
}

// ProcessIncoming reads from the peer's incoming channel and reassembles chunked messages.
// Returns complete messages on the output channel.
func (m *DataChannelManager) ProcessIncoming(output chan<- *ReassembledMessage) {
	for raw := range m.peer.Receive() {
		frame, err := decodeFrame(raw)
		if err != nil {
			log.Error().Err(err).Msg("Failed to decode incoming frame")
			continue
		}

		if frame.RequestID == 0 {
			// Heartbeat message, discard to keep connection alive
			continue
		}

		// Single-chunk message (most common case)
		if frame.TotalChunks == 1 {
			output <- &ReassembledMessage{
				RequestID: frame.RequestID,
				Data:      frame.Payload,
			}
			continue
		}

		// Multi-chunk: add to reassembly buffer
		key := frame.RequestID
		bufRaw, _ := m.reassembly.LoadOrStore(key, &reassemblyBuffer{
			totalChunks: frame.TotalChunks,
			received:    make(map[uint32][]byte),
		})
		buf := bufRaw.(*reassemblyBuffer)

		buf.mu.Lock()
		buf.received[frame.ChunkIndex] = frame.Payload

		if uint32(len(buf.received)) == buf.totalChunks {
			// All chunks received, reassemble
			totalSize := 0
			for _, chunk := range buf.received {
				totalSize += len(chunk)
			}

			data := make([]byte, 0, totalSize)
			for i := uint32(0); i < buf.totalChunks; i++ {
				data = append(data, buf.received[i]...)
			}

			buf.mu.Unlock()
			m.reassembly.Delete(key)

			output <- &ReassembledMessage{
				RequestID: frame.RequestID,
				Data:      data,
			}

			log.Debug().
				Uint32("request_id", frame.RequestID).
				Int("total_size", totalSize).
				Uint32("chunks", frame.TotalChunks).
				Msg("Message reassembled")
		} else {
			buf.mu.Unlock()
		}
	}
}

// ReassembledMessage represents a fully reassembled message from chunked transmission.
type ReassembledMessage struct {
	RequestID uint32
	Data      []byte
}

// encodeFrame serializes a Frame to bytes for transmission.
func encodeFrame(f *Frame) []byte {
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint32(buf[0:4], f.RequestID)
	binary.BigEndian.PutUint32(buf[4:8], f.ChunkIndex)
	binary.BigEndian.PutUint32(buf[8:12], f.TotalChunks)
	binary.BigEndian.PutUint32(buf[12:16], uint32(len(f.Payload)))
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// decodeFrame deserializes bytes back into a Frame.
func decodeFrame(data []byte) (*Frame, error) {
	if len(data) < HeaderSize {
		return nil, fmt.Errorf("frame too short: %d bytes (minimum %d)", len(data), HeaderSize)
	}

	payloadLen := binary.BigEndian.Uint32(data[12:16])
	if len(data) < int(HeaderSize+payloadLen) {
		return nil, fmt.Errorf("frame payload truncated: expected %d bytes, got %d",
			payloadLen, len(data)-HeaderSize)
	}

	return &Frame{
		RequestID:   binary.BigEndian.Uint32(data[0:4]),
		ChunkIndex:  binary.BigEndian.Uint32(data[4:8]),
		TotalChunks: binary.BigEndian.Uint32(data[8:12]),
		Payload:     data[HeaderSize : HeaderSize+payloadLen],
	}, nil
}
