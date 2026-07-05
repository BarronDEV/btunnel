package webrtc

import (
	"bytes"
	"testing"
)

func TestFrameEncodingDecoding(t *testing.T) {
	originalFrame := &Frame{
		RequestID:   42,
		ChunkIndex:  1,
		TotalChunks: 3,
		Payload:     []byte("test-payload-data"),
	}

	encoded := encodeFrame(originalFrame)

	decoded, err := decodeFrame(encoded)
	if err != nil {
		t.Fatalf("Failed to decode frame: %v", err)
	}

	if decoded.RequestID != originalFrame.RequestID {
		t.Errorf("Expected RequestID %d, got %d", originalFrame.RequestID, decoded.RequestID)
	}

	if decoded.ChunkIndex != originalFrame.ChunkIndex {
		t.Errorf("Expected ChunkIndex %d, got %d", originalFrame.ChunkIndex, decoded.ChunkIndex)
	}

	if decoded.TotalChunks != originalFrame.TotalChunks {
		t.Errorf("Expected TotalChunks %d, got %d", originalFrame.TotalChunks, decoded.TotalChunks)
	}

	if !bytes.Equal(decoded.Payload, originalFrame.Payload) {
		t.Errorf("Expected Payload %s, got %s", string(originalFrame.Payload), string(decoded.Payload))
	}
}

func TestDecodeFrameTooShort(t *testing.T) {
	data := make([]byte, HeaderSize-1)
	_, err := decodeFrame(data)
	if err == nil {
		t.Error("Expected error when decoding too short frame, got nil")
	}
}

func TestDecodeFrameTruncatedPayload(t *testing.T) {
	frame := &Frame{
		RequestID:   1,
		ChunkIndex:  0,
		TotalChunks: 1,
		Payload:     []byte("hello"),
	}
	encoded := encodeFrame(frame)
	
	// Truncate the payload bytes
	truncated := encoded[:len(encoded)-2]
	_, err := decodeFrame(truncated)
	if err == nil {
		t.Error("Expected error when decoding truncated payload frame, got nil")
	}
}
