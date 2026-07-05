/**
 * BTunnel Service Worker
 * 
 * Intercepts all fetch requests from the browser and routes them
 * through the WebRTC P2P DataChannel to the host CLI, which proxies
 * them to the local Docker container or service.
 * 
 * Flow:
 *   1. Page registers this SW and sends a MessagePort via INIT_TUNNEL
 *   2. SW intercepts fetch events for proxied requests
 *   3. Serializes HTTP request → sends via MessagePort → page → DataChannel → Host CLI
 *   4. Host CLI proxies to local service, sends response back
 *   5. SW reconstructs Response object and returns to the browser
 */

// ─────────────────────────────────────────────────
// State
// ─────────────────────────────────────────────────

/** @type {MessagePort|null} */
let tunnelPort = null;

/** @type {Map<string, {resolve: Function, reject: Function}>} */
const pendingRequests = new Map();

/** @type {number} */
let requestCounter = 0;

/** @type {Set<string>} */
const BYPASS_PATHS = new Set([
    '/sw.js',
    '/webrtc-client.js',
]);

// ─────────────────────────────────────────────────
// Lifecycle Events
// ─────────────────────────────────────────────────

/**
 * Install event — immediately activate the new SW.
 */
self.addEventListener('install', (event) => {
    console.log('[btunnel-sw] Installing Service Worker');
    // Skip waiting to activate immediately
    self.skipWaiting();
});

/**
 * Activate event — claim all open clients.
 */
self.addEventListener('activate', (event) => {
    console.log('[btunnel-sw] Service Worker activated');
    // Take control of all pages immediately
    event.waitUntil(self.clients.claim());
});

// ─────────────────────────────────────────────────
// Message Handler (from page)
// ─────────────────────────────────────────────────

/**
 * Listen for messages from the main page.
 * The page sends INIT_TUNNEL with a MessagePort for bidirectional communication.
 */
self.addEventListener('message', (event) => {
    const { type, port } = event.data;

    if (type === 'INIT_TUNNEL' && port) {
        console.log('[btunnel-sw] Tunnel port received');
        tunnelPort = port;

        // Listen for responses from the page (which come from the DataChannel)
        tunnelPort.onmessage = (msgEvent) => {
            const { id, status, headers, body } = msgEvent.data;

            const pending = pendingRequests.get(id);
            if (pending) {
                pendingRequests.delete(id);
                pending.resolve({ status, headers, body });
            } else {
                console.warn('[btunnel-sw] Received response for unknown request:', id);
            }
        };

        tunnelPort.start();
    }

    if (type === 'PING_TUNNEL') {
        if (!tunnelPort) {
            console.log('[btunnel-sw] Heartbeat detected but tunnelPort is missing. Requesting recovery...');
            self.clients.matchAll().then((clients) => {
                clients.forEach((client) => {
                    client.postMessage({ type: 'REQUEST_TUNNEL_PORT' });
                });
            });
        }
    }

    if (type === 'CLOSE_TUNNEL') {
        console.log('[btunnel-sw] Tunnel closed');
        tunnelPort = null;

        // Reject all pending requests
        for (const [id, pending] of pendingRequests) {
            pending.reject(new Error('Tunnel closed'));
        }
        pendingRequests.clear();
    }
});

// ─────────────────────────────────────────────────
// Fetch Interceptor
// ─────────────────────────────────────────────────

/**
 * Intercept fetch events and proxy them through the P2P tunnel.
 */
self.addEventListener('fetch', (event) => {
    const url = new URL(event.request.url);

    // Only proxy same-origin requests
    if (url.origin !== self.location.origin) {
        return; // Let cross-origin requests pass through normally
    }

    // Bypass our own files
    if (shouldBypass(url.pathname)) {
        return;
    }

    // If tunnel is not active, return a helpful error
    if (!tunnelPort) {
        event.respondWith(
            new Response(
                JSON.stringify({
                    error: 'btunnel not connected',
                    message: 'The P2P tunnel is not active. Please connect first.',
                }),
                {
                    status: 503,
                    headers: { 'Content-Type': 'application/json' },
                }
            )
        );
        return;
    }

    // Proxy the request through the tunnel
    event.respondWith(proxyThroughTunnel(event.request, url));
});

// ─────────────────────────────────────────────────
// Proxy Logic
// ─────────────────────────────────────────────────

/**
 * Proxy a fetch request through the P2P tunnel.
 * @param {Request} request - The intercepted fetch request
 * @param {URL} url - Parsed URL
 * @returns {Promise<Response>}
 */
async function proxyThroughTunnel(request, url) {
    const requestId = 'sw-req-' + (++requestCounter);

    try {
        // Serialize headers
        const headers = {};
        for (const [key, value] of request.headers.entries()) {
            headers[key] = value;
        }

        // Read body if present (POST, PUT, etc.)
        let body = null;
        if (request.method !== 'GET' && request.method !== 'HEAD') {
            try {
                const bodyBuffer = await request.arrayBuffer();
                if (bodyBuffer.byteLength > 0) {
                    // Convert to base64 for JSON transport
                    body = arrayBufferToBase64(bodyBuffer);
                }
            } catch (e) {
                // No body or body already consumed
            }
        }

        // Build the proxied URL path (relative to origin)
        const proxyUrl = url.pathname + url.search;

        // Send request to page via MessagePort
        const responseData = await sendTunnelRequest({
            id: requestId,
            method: request.method,
            url: proxyUrl,
            headers: headers,
            body: body,
        });

        // Build response headers
        const responseHeaders = new Headers();
        if (responseData.headers) {
            for (const [key, value] of Object.entries(responseData.headers)) {
                // Skip hop-by-hop headers that shouldn't be forwarded
                if (!isHopByHopHeader(key)) {
                    responseHeaders.set(key, value);
                }
            }
        }

        // Add CORS headers to allow the page to read the response
        responseHeaders.set('X-BTunnel-Proxied', 'true');

        // Build response body
        let responseBody = responseData.body;
        if (typeof responseBody === 'string' && responseBody !== '') {
            try {
                responseBody = base64ToArrayBuffer(responseBody);
            } catch (e) {
                console.error('[btunnel-sw] Failed to decode base64 response body:', e);
            }
        }

        return new Response(responseBody, {
            status: responseData.status || 200,
            statusText: getStatusText(responseData.status || 200),
            headers: responseHeaders,
        });

    } catch (err) {
        console.error('[btunnel-sw] Proxy error:', err);
        return new Response(
            JSON.stringify({
                error: 'btunnel proxy error',
                message: err.message,
                requestId: requestId,
            }),
            {
                status: 502,
                headers: { 'Content-Type': 'application/json' },
            }
        );
    }
}

/**
 * Send a request to the page (and ultimately through the DataChannel).
 * @param {Object} request - Serialized HTTP request
 * @returns {Promise<Object>} - Serialized HTTP response
 */
function sendTunnelRequest(request) {
    return new Promise((resolve, reject) => {
        if (!tunnelPort) {
            reject(new Error('Tunnel not connected'));
            return;
        }

        pendingRequests.set(request.id, { resolve, reject });

        // Send to the page
        tunnelPort.postMessage(request);

        // Timeout: 30 seconds
        setTimeout(() => {
            if (pendingRequests.has(request.id)) {
                pendingRequests.delete(request.id);
                reject(new Error(`Request timeout: ${request.method} ${request.url}`));
            }
        }, 30000);
    });
}

// ─────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────

/**
 * Check if a path should bypass the tunnel proxy.
 */
function shouldBypass(pathname) {
    if (BYPASS_PATHS.has(pathname)) return true;
    if (pathname.startsWith('/share/')) return true;
    return false;
}

/**
 * Check if a header is a hop-by-hop header (should not be forwarded).
 */
function isHopByHopHeader(name) {
    const hopByHop = new Set([
        'connection', 'keep-alive', 'proxy-authenticate',
        'proxy-authorization', 'te', 'trailers',
        'transfer-encoding', 'upgrade',
    ]);
    return hopByHop.has(name.toLowerCase());
}

/**
 * Check if a Content-Type indicates binary content.
 */
function isBinaryContentType(contentType) {
    if (!contentType) return false;
    return contentType.startsWith('image/') ||
        contentType.startsWith('audio/') ||
        contentType.startsWith('video/') ||
        contentType.startsWith('application/octet-stream') ||
        contentType.startsWith('application/pdf') ||
        contentType.startsWith('application/zip');
}

/**
 * Convert ArrayBuffer to base64 string.
 */
function arrayBufferToBase64(buffer) {
    const bytes = new Uint8Array(buffer);
    let binary = '';
    for (let i = 0; i < bytes.byteLength; i++) {
        binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary);
}

/**
 * Convert base64 string to ArrayBuffer.
 */
function base64ToArrayBuffer(base64) {
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i);
    }
    return bytes.buffer;
}

/**
 * Get HTTP status text for a given status code.
 */
function getStatusText(code) {
    const texts = {
        200: 'OK', 201: 'Created', 204: 'No Content',
        301: 'Moved Permanently', 302: 'Found', 304: 'Not Modified',
        400: 'Bad Request', 401: 'Unauthorized', 403: 'Forbidden',
        404: 'Not Found', 405: 'Method Not Allowed',
        500: 'Internal Server Error', 502: 'Bad Gateway',
        503: 'Service Unavailable', 504: 'Gateway Timeout',
    };
    return texts[code] || 'Unknown';
}
