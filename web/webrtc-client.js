/**
 * BTunnel WebRTC Client
 * 
 * Browser-side WebRTC client that establishes a P2P connection
 * to the host CLI via the signaling server. Handles SDP exchange,
 * ICE candidate gathering, DataChannel communication, and HTTP
 * request proxying over the encrypted tunnel.
 */

class BTunnelClient {
    /**
     * @param {Object} options
     * @param {string} options.signalingURL - WebSocket URL for signaling server
     * @param {string} options.token - Session token to join
     * @param {function} options.onStateChange - Callback for connection state changes
     * @param {function} options.onStats - Callback for stats updates
     * @param {function} options.onLog - Callback for log messages
     */
    constructor(options) {
        this.signalingURL = options.signalingURL;
        this.token = options.token;
        this.onStateChange = options.onStateChange || (() => {});
        this.onStats = options.onStats || (() => {});
        this.onLog = options.onLog || (() => {});

        this.ws = null;
        this.pc = null;
        this.dataChannels = [];
        this.sessionId = null;
        this.iceServers = [];

        // Stats tracking
        this._bytesSent = 0;
        this._bytesReceived = 0;
        this._rtt = -1;

        // Pending HTTP proxy requests: id -> { resolve, reject }
        this._pendingRequests = new Map();

        // Request ID counter
        this._requestIdCounter = 0;

        // Round-robin counter for DataChannel selection
        this._rrCounter = 0;

        // Message reassembly buffer for chunked messages
        this._reassemblyBuffer = new Map();

        // Virtual WebSocket connections map
        this._virtualWebSockets = new Map();
        this._virtualSocketIdCounter = 0;

        // Frame header size (matches Go side)
        this.HEADER_SIZE = 16;
        this.MAX_CHUNK_SIZE = 16 * 1024;

        this._heartbeatWorker = null;
    }

    // ─────────────────────────────────────────────────
    // Signaling
    // ─────────────────────────────────────────────────

    /**
     * Connect to the signaling server and join the session.
     * @returns {Promise<void>}
     */
    connectSignaling() {
        return new Promise((resolve, reject) => {
            this.onLog('Connecting to signaling server...', 'info');

            this.ws = new WebSocket(this.signalingURL);

            const timeout = setTimeout(() => {
                reject(new Error('Signaling server connection timeout'));
                this.ws.close();
            }, 10000);

            this.ws.onopen = () => {
                clearTimeout(timeout);
                this.onLog('Connected to signaling server', 'success');

                // Send join message with token
                this._sendSignaling({
                    type: 'join',
                    token: this.token,
                    timestamp: Math.floor(Date.now() / 1000),
                });
            };

            this.ws.onmessage = (event) => {
                const msg = JSON.parse(event.data);
                this._handleSignalingMessage(msg, resolve, reject);
            };

            this.ws.onerror = (err) => {
                clearTimeout(timeout);
                this.onLog('Signaling server error', 'error');
                reject(new Error('Failed to connect to signaling server'));
            };

            this.ws.onclose = () => {
                this.onLog('Signaling connection closed', 'info');
            };
        });
    }

    /**
     * Handle incoming signaling messages.
     */
    _handleSignalingMessage(msg, resolveConnect, rejectConnect) {
        switch (msg.type) {
            case 'session-created':
                // We received session info after joining
                const payload = typeof msg.payload === 'string' 
                    ? JSON.parse(msg.payload) 
                    : msg.payload;
                this.sessionId = payload.sessionid || msg.sessionid;
                this.iceServers = (payload.ice_servers || []).map(server => {
                    if (typeof server === 'string') {
                        return { urls: server };
                    }
                    return server;
                });
                this.onLog(`Joined session: ${this.sessionId.slice(0, 12)}...`, 'info');
                if (resolveConnect) resolveConnect();
                break;

            case 'offer':
                // Host sent us an SDP offer
                const offerPayload = typeof msg.payload === 'string'
                    ? JSON.parse(msg.payload)
                    : msg.payload;
                this._handleOffer(offerPayload);
                break;

            case 'ice-candidate':
                // Received ICE candidate from host
                const icePayload = typeof msg.payload === 'string'
                    ? JSON.parse(msg.payload)
                    : msg.payload;
                this._handleRemoteICECandidate(icePayload);
                break;

            case 'error':
                const errPayload = typeof msg.payload === 'string'
                    ? JSON.parse(msg.payload)
                    : msg.payload;
                const errMsg = errPayload ? errPayload.message : 'Unknown error';
                this.onLog(`Server error: ${errMsg}`, 'error');
                if (rejectConnect) rejectConnect(new Error(errMsg));
                break;

            default:
                this.onLog(`Unknown message type: ${msg.type}`, 'info');
        }
    }

    /**
     * Send a message via the signaling WebSocket.
     */
    _sendSignaling(msg) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify(msg));
        }
    }

    // ─────────────────────────────────────────────────
    // WebRTC Peer Connection
    // ─────────────────────────────────────────────────

    /**
     * Set up the WebRTC PeerConnection and wait for the SDP offer from host.
     * @returns {Promise<void>}
     */
    establishPeerConnection() {
        return new Promise((resolve, reject) => {
            this.onLog('Setting up WebRTC peer connection...', 'info');

            // Default STUN servers if none provided
            const iceServers = this.iceServers.length > 0 
                ? this.iceServers 
                : [
                    { urls: 'stun:stun.l.google.com:19302' },
                    { urls: 'stun:stun1.l.google.com:19302' },
                ];

            this.pc = new RTCPeerConnection({ iceServers });

            // ICE candidate handler
            this.pc.onicecandidate = (event) => {
                if (event.candidate) {
                    this._sendSignaling({
                        type: 'ice-candidate',
                        sessionid: this.sessionId,
                        payload: {
                            candidate: event.candidate.candidate,
                            sdpMid: event.candidate.sdpMid,
                            sdpMLineIndex: event.candidate.sdpMLineIndex,
                        },
                        timestamp: Math.floor(Date.now() / 1000),
                    });
                }
            };

            // Connection state change handler
            this.pc.onconnectionstatechange = () => {
                const state = this.pc.connectionState;
                this.onLog(`Connection state: ${state}`, 'info');

                if (state === 'connected') {
                    resolve();
                } else if (state === 'failed' || state === 'closed') {
                    this.onStateChange('disconnected');
                    reject(new Error(`P2P connection ${state}`));
                }
            };

            // ICE connection state for debugging
            this.pc.oniceconnectionstatechange = () => {
                this.onLog(`ICE state: ${this.pc.iceConnectionState}`, 'info');
            };

            // DataChannel handler (for channels created by host)
            this.pc.ondatachannel = (event) => {
                this.onLog(`Data channel received: ${event.channel.label}`, 'info');
                this._setupDataChannel(event.channel);
            };

            // Store resolve/reject for use in _handleOffer
            this._peerResolve = resolve;
            this._peerReject = reject;

            // Timeout for peer connection
            this._peerTimeout = setTimeout(() => {
                reject(new Error('P2P connection timeout (30s)'));
            }, 30000);
        });
    }

    /**
     * Handle an SDP offer from the host.
     */
    async _handleOffer(payload) {
        try {
            this.onLog('Received SDP offer from host', 'info');

            await this.pc.setRemoteDescription(new RTCSessionDescription({
                type: 'offer',
                sdp: payload.sdp,
            }));

            const answer = await this.pc.createAnswer();
            await this.pc.setLocalDescription(answer);

            this._sendSignaling({
                type: 'answer',
                sessionid: this.sessionId,
                payload: {
                    type: 'answer',
                    sdp: answer.sdp,
                },
                timestamp: Math.floor(Date.now() / 1000),
            });

            this.onLog('SDP answer sent', 'info');
        } catch (err) {
            this.onLog(`SDP exchange failed: ${err.message}`, 'error');
            if (this._peerReject) this._peerReject(err);
        }
    }

    /**
     * Handle a remote ICE candidate.
     */
    async _handleRemoteICECandidate(payload) {
        try {
            if (this.pc && payload.candidate) {
                await this.pc.addIceCandidate(new RTCIceCandidate({
                    candidate: payload.candidate,
                    sdpMid: payload.sdpMid,
                    sdpMLineIndex: payload.sdpMLineIndex,
                }));
                this.onLog('Added remote ICE candidate', 'info');
            }
        } catch (err) {
            this.onLog(`Failed to add ICE candidate: ${err.message}`, 'error');
        }
    }

    // ─────────────────────────────────────────────────
    // Data Channels
    // ─────────────────────────────────────────────────

    /**
     * Set up event handlers for a DataChannel.
     */
    _setupDataChannel(dc) {
        dc.binaryType = 'arraybuffer';

        dc.onopen = () => {
            this.onLog(`DataChannel "${dc.label}" opened`, 'success');
            this.dataChannels.push(dc);
            this._startHeartbeat();
        };

        dc.onmessage = (event) => {
            this._handleDataChannelMessage(event.data);
        };

        dc.onclose = () => {
            this.onLog(`DataChannel "${dc.label}" closed`, 'info');
            this.dataChannels = this.dataChannels.filter(c => c !== dc);
            this._stopHeartbeat();
        };

        dc.onerror = (err) => {
            this.onLog(`DataChannel error: ${err.message || 'unknown'}`, 'error');
        };
    }

    _startHeartbeat() {
        if (this._heartbeatWorker) return;

        // Create an inline Web Worker that ticks every 5 seconds.
        // Web Workers are NOT throttled when the tab is in the background,
        // unlike setInterval which Chrome throttles to ~1 min in background tabs.
        // This is critical: without timely pings, NAT/CGNAT bindings expire
        // after ~30-60s of inactivity and the UDP tunnel dies.
        const workerCode = `
            let interval = null;
            self.onmessage = function(e) {
                if (e.data === 'start') {
                    if (interval) clearInterval(interval);
                    interval = setInterval(function() { self.postMessage('tick'); }, 5000);
                } else if (e.data === 'stop') {
                    if (interval) { clearInterval(interval); interval = null; }
                }
            };
        `;
        const blob = new Blob([workerCode], { type: 'application/javascript' });
        this._heartbeatWorker = new Worker(URL.createObjectURL(blob));
        this._heartbeatWorker.onmessage = () => {
            this._sendPing();
        };
        this._heartbeatWorker.postMessage('start');
    }

    _stopHeartbeat() {
        if (this.dataChannels.length === 0 && this._heartbeatWorker) {
            this._heartbeatWorker.postMessage('stop');
            this._heartbeatWorker.terminate();
            this._heartbeatWorker = null;
        }
    }

    _sendPing() {
        if (this.dataChannels.length === 0) return;
        const pingPayload = '{"type":"ping"}';
        const payloadBytes = new TextEncoder().encode(pingPayload);
        
        const frame = new ArrayBuffer(this.HEADER_SIZE + payloadBytes.length);
        const view = new DataView(frame);
        view.setUint32(0, 0); // RequestID: 0 (ping)
        view.setUint32(4, 0); // ChunkIndex: 0
        view.setUint32(8, 1); // TotalChunks: 1
        view.setUint32(12, payloadBytes.length); // PayloadLen
        new Uint8Array(frame, this.HEADER_SIZE).set(payloadBytes);

        const dc = this.dataChannels[this._rrCounter % this.dataChannels.length];
        this._rrCounter++;
        if (dc && dc.readyState === 'open') {
            try {
                dc.send(frame);
            } catch (err) {
                // Ignore send errors
            }
        }
    }

    /**
     * Wait until at least one DataChannel is open.
     * @returns {Promise<void>}
     */
    waitForDataChannels() {
        return new Promise((resolve, reject) => {
            // Check if already have open channels
            if (this.dataChannels.length > 0) {
                resolve();
                return;
            }

            const checkInterval = setInterval(() => {
                if (this.dataChannels.length > 0) {
                    clearInterval(checkInterval);
                    clearTimeout(timeout);
                    resolve();
                }
            }, 100);

            const timeout = setTimeout(() => {
                clearInterval(checkInterval);
                reject(new Error('DataChannel open timeout (15s)'));
            }, 15000);
        });
    }

    /**
     * Handle incoming DataChannel binary message (framed protocol).
     */
    _handleDataChannelMessage(data) {
        const arrayBuffer = data instanceof ArrayBuffer ? data : data.buffer;
        this._bytesReceived += arrayBuffer.byteLength;

        // Decode frame header
        const view = new DataView(arrayBuffer);
        if (arrayBuffer.byteLength < this.HEADER_SIZE) {
            this.onLog('Received malformed frame (too short)', 'error');
            return;
        }

        const requestId = view.getUint32(0);
        if (requestId === 0) {
            // Heartbeat ping, ignore to keep connection alive
            return;
        }
        const chunkIndex = view.getUint32(4);
        const totalChunks = view.getUint32(8);
        const payloadLen = view.getUint32(12);

        const payload = new Uint8Array(arrayBuffer, this.HEADER_SIZE, payloadLen);

        // Single-chunk message (most common)
        if (totalChunks === 1) {
            this._processCompleteMessage(requestId, payload);
            return;
        }

        // Multi-chunk: reassemble
        if (!this._reassemblyBuffer.has(requestId)) {
            this._reassemblyBuffer.set(requestId, {
                totalChunks: totalChunks,
                received: new Map(),
            });
        }

        const buffer = this._reassemblyBuffer.get(requestId);
        buffer.received.set(chunkIndex, payload);

        if (buffer.received.size === buffer.totalChunks) {
            // All chunks received, reassemble
            let totalSize = 0;
            buffer.received.forEach(chunk => totalSize += chunk.length);

            const assembled = new Uint8Array(totalSize);
            let offset = 0;
            for (let i = 0; i < buffer.totalChunks; i++) {
                const chunk = buffer.received.get(i);
                assembled.set(chunk, offset);
                offset += chunk.length;
            }

            this._reassemblyBuffer.delete(requestId);
            this._processCompleteMessage(requestId, assembled);
        }
    }

    _processCompleteMessage(requestId, payload) {
        try {
            const text = new TextDecoder().decode(payload);
            const response = JSON.parse(text);

            // Check if this is a response to a pending proxy request
            const pending = this._pendingRequests.get(response.id);
            if (pending) {
                this._pendingRequests.delete(response.id);
                pending.resolve(response);
                return;
            }

            // Route WebSocket server push events
            if (response.id && response.id.startsWith('ws-socket-')) {
                const ws = this._virtualWebSockets.get(response.id);
                if (ws) {
                    ws._handleIncomingEvent(response);
                }
            }
        } catch (err) {
            this.onLog(`Failed to process message: ${err.message}`, 'error');
        }
    }

    // ─────────────────────────────────────────────────
    // HTTP Proxy
    // ─────────────────────────────────────────────────

    /**
     * Send an HTTP request through the P2P tunnel.
     * @param {Object} request - { id, method, url, headers, body }
     * @returns {Promise<Object>} - { id, status_code, headers, body }
     */
    proxyRequest(request) {
        return new Promise((resolve, reject) => {
            if (this.dataChannels.length === 0) {
                reject(new Error('No open DataChannels'));
                return;
            }

            // Generate request ID if not provided
            if (!request.id) {
                request.id = 'req-' + (++this._requestIdCounter);
            }

            // Store pending request
            this._pendingRequests.set(request.id, { resolve, reject });

            // Serialize request to JSON
            const jsonStr = JSON.stringify({
                id: request.id,
                method: request.method || 'GET',
                url: request.url || '/',
                headers: request.headers || {},
                body: request.body || null,
            });

            const payload = new TextEncoder().encode(jsonStr);

            // Send via DataChannel with framing
            this._sendFramed(payload);

            // Timeout for response
            setTimeout(() => {
                if (this._pendingRequests.has(request.id)) {
                    this._pendingRequests.delete(request.id);
                    reject(new Error('Proxy request timeout (30s)'));
                }
            }, 30000);
        });
    }

    /**
     * Send framed data over DataChannel with chunking.
     */
    _sendFramed(payload) {
        const maxPayload = this.MAX_CHUNK_SIZE - this.HEADER_SIZE;
        const totalChunks = Math.ceil(payload.length / maxPayload);
        const requestId = ++this._requestIdCounter;

        for (let i = 0; i < totalChunks; i++) {
            const start = i * maxPayload;
            const end = Math.min(start + maxPayload, payload.length);
            const chunkPayload = payload.slice(start, end);

            // Build frame: [requestID(4)][chunkIndex(4)][totalChunks(4)][payloadLen(4)][payload]
            const frame = new ArrayBuffer(this.HEADER_SIZE + chunkPayload.length);
            const view = new DataView(frame);
            view.setUint32(0, requestId);
            view.setUint32(4, i);
            view.setUint32(8, totalChunks);
            view.setUint32(12, chunkPayload.length);
            new Uint8Array(frame, this.HEADER_SIZE).set(chunkPayload);

            // Round-robin channel selection
            const dc = this.dataChannels[this._rrCounter % this.dataChannels.length];
            this._rrCounter++;

            if (dc.readyState === 'open') {
                dc.send(frame);
                this._bytesSent += frame.byteLength;
            }
        }
    }

    // ─────────────────────────────────────────────────
    // Stats
    // ─────────────────────────────────────────────────

    /**
     * Get current tunnel statistics.
     * @returns {Object} - { rtt, bytesSent, bytesReceived }
     */
    getStats() {
        // Try to get RTT from WebRTC stats
        if (this.pc) {
            this.pc.getStats().then(stats => {
                stats.forEach(report => {
                    if (report.type === 'candidate-pair' && report.state === 'succeeded') {
                        this._rtt = Math.round(report.currentRoundTripTime * 1000);
                    }
                });
            });
        }

        return {
            rtt: this._rtt,
            bytesSent: this._bytesSent,
            bytesReceived: this._bytesReceived,
        };
    }

    // ─────────────────────────────────────────────────
    // Cleanup
    // ─────────────────────────────────────────────────

    /**
     * Close all connections.
     */
    close() {
        // Close DataChannels
        this.dataChannels.forEach(dc => {
            try { dc.close(); } catch (e) {}
        });
        this.dataChannels = [];

        // Close PeerConnection
        if (this.pc) {
            try { this.pc.close(); } catch (e) {}
            this.pc = null;
        }

        // Close WebSocket
        if (this.ws) {
            try { this.ws.close(); } catch (e) {}
            this.ws = null;
        }

        // Clear pending requests
        this._pendingRequests.forEach(({ reject }) => {
            reject(new Error('Connection closed'));
        });
        this._pendingRequests.clear();
        this._reassemblyBuffer.clear();

        // Close virtual WebSockets
        this._virtualWebSockets.forEach(ws => ws.close());
        this._virtualWebSockets.clear();

        if (this._peerTimeout) {
            clearTimeout(this._peerTimeout);
        }

        this.onLog('All connections closed', 'info');
    }

    /**
     * Override window.WebSocket to proxy traffic through the P2P WebRTC DataChannel.
     */
    enableWebSocketOverride() {
        const client = this;
        window.OriginalWebSocket = window.WebSocket;

        class VirtualWebSocket {
            constructor(url, protocols) {
                this.url = url;
                this.protocols = protocols;
                this.readyState = 0; // CONNECTING
                this.bufferedAmount = 0;
                this.extensions = "";
                this.binaryType = "blob";

                // Setup unique socket ID
                this._id = 'ws-socket-' + (++client._virtualSocketIdCounter);
                client._virtualWebSockets.set(this._id, this);

                // Event handlers
                this.onopen = null;
                this.onmessage = null;
                this.onerror = null;
                this.onclose = null;

                // Initiate connection over DataChannel
                this._connect();
            }

            async _connect() {
                try {
                    // Send WS_CONNECT request
                    const resp = await client.proxyRequest({
                        id: this._id,
                        method: 'WS_CONNECT',
                        url: this.url,
                        headers: {
                            'Sec-WebSocket-Protocol': Array.isArray(this.protocols) 
                                ? this.protocols.join(', ') 
                                : (this.protocols || '')
                        }
                    });

                    if (resp.status_code === 200 || resp.status_code === 101) {
                        this.readyState = 1; // OPEN
                        if (typeof this.onopen === 'function') {
                            this.onopen({ type: 'open' });
                        }
                    } else {
                        throw new Error('WebSocket connection rejected by host: ' + (resp.body || resp.status_code));
                    }
                } catch (err) {
                    this.readyState = 3; // CLOSED
                    client.onLog(`Virtual WebSocket ${this._id} connection failed: ${err.message}`, 'error');
                    if (typeof this.onerror === 'function') {
                        this.onerror(err);
                    }
                    if (typeof this.onclose === 'function') {
                        this.onclose({ code: 1006, reason: err.message, wasClean: false });
                    }
                    client._virtualWebSockets.delete(this._id);
                }
            }

            send(data) {
                if (this.readyState !== 1) {
                    throw new Error('WebSocket is not in OPEN state');
                }

                let body = data;
                let isBinary = false;

                if (data instanceof ArrayBuffer) {
                    body = client.arrayBufferToBase64(data);
                    isBinary = true;
                } else if (ArrayBuffer.isView(data)) {
                    body = client.arrayBufferToBase64(data.buffer);
                    isBinary = true;
                }

                // Send WS_DATA frame
                client.proxyRequest({
                    id: this._id,
                    method: 'WS_DATA',
                    url: this.url,
                    body: body,
                    headers: { 'X-Binary': isBinary ? 'true' : 'false' }
                }).catch(err => {
                    client.onLog(`Failed to send data on virtual WS ${this._id}: ${err.message}`, 'error');
                });
            }

            close(code, reason) {
                if (this.readyState === 2 || this.readyState === 3) return;
                this.readyState = 2; // CLOSING

                client.proxyRequest({
                    id: this._id,
                    method: 'WS_CLOSE',
                    url: this.url,
                    headers: { 'X-Code': String(code || 1000), 'X-Reason': reason || '' }
                }).finally(() => {
                    this.readyState = 3; // CLOSED
                    if (typeof this.onclose === 'function') {
                        this.onclose({ code: code || 1000, reason: reason || "", wasClean: true });
                    }
                    client._virtualWebSockets.delete(this._id);
                });
            }

            _handleIncomingEvent(event) {
                if (event.method === 'WS_DATA') {
                    let data = event.body;
                    if (event.headers && event.headers['X-Binary'] === 'true') {
                        data = client.base64ToArrayBuffer(data);
                        if (this.binaryType === 'blob') {
                            data = new Blob([data]);
                        }
                    }
                    if (typeof this.onmessage === 'function') {
                        this.onmessage({ data: data });
                    }
                } else if (event.method === 'WS_CLOSE') {
                    this.readyState = 3; // CLOSED
                    const code = event.headers ? parseInt(event.headers['X-Code'] || '1000', 10) : 1000;
                    const reason = event.headers ? event.headers['X-Reason'] || '' : '';
                    if (typeof this.onclose === 'function') {
                        this.onclose({ code: code, reason: reason, wasClean: true });
                    }
                    client._virtualWebSockets.delete(this._id);
                }
            }
        }

        window.WebSocket = VirtualWebSocket;
        this.onLog('WebSocket API virtualized and overridden successfully', 'success');
    }
}
