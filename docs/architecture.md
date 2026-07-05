# 🧬 BTunnel: Architecture & System Design

This document details the architectural layout, communications protocols, and design patterns used in **BTunnel**.

---

## 1. High-Level Topology

BTunnel connects a **Host CLI** (the provider exposing a service) and a **Client** (either another CLI or a web browser) using end-to-end encrypted WebRTC P2P DataChannels.

```mermaid
graph TD
    subgraph Local Environment (Host)
        HostCLI[Host CLI Go]
        Docker[Docker Containers]
        TargetApp[Local Port 8080]
        HostCLI -->|Inspects & Connects| Docker
        HostCLI -->|Proxies| TargetApp
    end

    subgraph Signaling Layer
        SigServer[Signaling Server Go]
    end

    subgraph Remote Environment (Client)
        ClientCLI[Client CLI Go]
        Browser[Client Browser]
        SW[Service Worker]
        Browser -->|Fetches| SW
        SW -->|MessagePort| Browser
    end

    HostCLI <-->|WebSocket SDP/ICE| SigServer
    ClientCLI <-->|WebSocket SDP/ICE| SigServer
    Browser <-->|WebSocket SDP/ICE| SigServer

    HostCLI <-->|WebRTC DataChannel P2P| ClientCLI
    HostCLI <-->|WebRTC DataChannel P2P| Browser
```

---

## 2. Communications Flow (The P2P Dance)

The connection setup is split into the signaling phase (via WebSocket) and the active P2P phase (direct connection).

### 2.1 Signaling Sequence

```mermaid
sequenceDiagram
    participant Host as Host CLI
    participant Sig as Signaling Server
    participant Client as Client (CLI or Browser)
    
    Host->>Sig: CREATE session (Mode, Target)
    Sig->>Sig: Generate cryptographically secure Token (bt-...)
    Sig-->>Host: SESSION_CREATED (SessionID, ICE Servers)
    Note over Host: Awaiting Client Join...
    
    Client->>Sig: JOIN session (Token)
    Sig->>Sig: Mark Token as consumed (Single-use)
    Sig-->>Host: PEER_JOINED
    Sig-->>Client: SESSION_CREATED (SessionID, ICE Servers)
    
    Host->>Sig: Relays SDP Offer
    Sig-->>Client: Relays SDP Offer
    Client->>Sig: Relays SDP Answer
    Sig-->>Host: Relays SDP Answer
    
    par ICE Gathering
        Host->>Sig: ICE Candidate
        Sig-->>Client: ICE Candidate
    and
        Client->>Sig: ICE Candidate
        Sig-->>Host: ICE Candidate
    end
    
    Note over Host, Client: Direct UDP Hole Punching (STUN)
    Host<-->Client: DTLS Handshake (Direct P2P Link Established)
    
    Note over Sig: Websocket connections closed or put to idle
```

---

## 3. WebRTC Multiplexing & Framing Protocol

P2P DataChannels are limited by SCTP packet sizing and Head-of-Line blocking (if a single channel is overwhelmed). To solve this:
1. **Parallel Channels**: We open up to 4-6 parallel DataChannels (`btunnel-dc-0`, `btunnel-dc-1`, etc.).
2. **Round-Robin distribution**: Outgoing requests/packets are spread round-robin across active channels.
3. **Chunking**: Data payloads are chunked to `16 KB` segments to avoid buffer overflows.

### 3.1 Frame Structure

Every payload sent over a DataChannel is prefixed with a `16-byte` header:

| Field | Size | Offset | Description |
|---|---|---|---|
| `RequestID` | 4 bytes | 0-3 | Uniquely identifies request/response payload stream |
| `ChunkIndex` | 4 bytes | 4-7 | Index of current chunk (0-based) |
| `TotalChunks` | 4 bytes | 8-11 | Total chunk count for the message |
| `PayloadLen` | 4 bytes | 12-15 | Length of actual payload following the header |
| `Payload` | Variable | 16+ | Raw data content |

---

## 4. Operational Modes

### 4.1 Mesh Mode (CLI-to-CLI)
- Ideal for TCP/UDP traffic like SSH, Minecraft server (`localhost:25565`), or databases.
- The client binds a local port and routes all packets into the WebRTC DataChannel. The host side decodes them and writes them to the target network/port.

### 4.2 Web Mode (CLI-to-Browser)
- Zero-installation needed for the viewer.
- The browser accesses `https://btunnel.live/share/<token>` which serves a modern SPA dashboard.
- A **Service Worker (`sw.js`)** is registered to intercept all browser `fetch` requests.
- The Service Worker serializes the HTTP request into JSON, pipes it via a `MessagePort` to the client page, which forwards it over WebRTC to the Host CLI.
- The Host CLI sends it to the local web server, receives the response, and replies back.

```
[Browser Request] ──> [Service Worker] ──> [webrtc-client.js] ──> (WebRTC) ──> [Host CLI] ──> [Local HTTP App]
```

---

## 5. Security & Isolation

- **E2EE (End-to-End Encryption)**: All transport data is encrypted via DTLS 1.3. Even if traffic falls back to a TURN server, the TURN server cannot decrypt the DTLS layer.
- **Docker Sidecar Isolation**: The Host CLI launches a temporary sidecar container (`btunnel-sidecar-<network-name>`) connected to the specified Docker network. The host's host network remains isolated; only the targeted Docker containers are accessible.
- **Single-use Tokens**: Tokens expire in 5 minutes and are deleted from the signaling memory immediately upon use.
