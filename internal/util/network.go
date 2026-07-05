package util

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// GetLocalIP returns the preferred outbound LAN IP address of the machine.
// It works by establishing a UDP "connection" to a public address (no actual
// packets are sent) and reading the source address chosen by the OS.
func GetLocalIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 3*time.Second)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to determine local IP via UDP dial")
		return getFallbackLocalIP()
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// getFallbackLocalIP iterates network interfaces to find a non-loopback IPv4.
func getFallbackLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// GetPublicIP attempts to discover the machine's public (WAN) IP address.
// It tries multiple free HTTP-based IP discovery services as fallbacks.
// Returns empty string if all attempts fail.
func GetPublicIP() string {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, svc := range services {
		resp, err := client.Get(svc)
		if err != nil {
			log.Debug().Err(err).Str("service", svc).Msg("Public IP service failed")
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		if err != nil {
			continue
		}

		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}

	log.Debug().Msg("Could not determine public IP from any service")
	return ""
}

// FormatToken creates the enhanced token format that embeds connection info.
// Format: bt-<random-token>@<ip>:<port>
func FormatToken(token, ip string, port int) string {
	return fmt.Sprintf("%s@%s:%d", token, ip, port)
}

// ParseToken parses the enhanced token format to extract the base token and
// the signaling server address.
// Input:  "bt-abc123def456@192.168.1.50:9090"
// Output: token="bt-abc123def456", signalingAddr="192.168.1.50:9090", ok=true
//
// Also supports legacy format (no @ sign):
// Input:  "bt-abc123def456"
// Output: token="bt-abc123def456", signalingAddr="", ok=false
func ParseToken(fullToken string) (token string, signalingAddr string, hasAddr bool) {
	atIdx := strings.LastIndex(fullToken, "@")
	if atIdx == -1 {
		// Legacy format — no embedded address
		return fullToken, "", false
	}

	token = fullToken[:atIdx]
	signalingAddr = fullToken[atIdx+1:]

	// Validate that the address part looks like host:port
	if _, _, err := net.SplitHostPort(signalingAddr); err != nil {
		// Invalid address format, treat entire string as legacy token
		return fullToken, "", false
	}

	return token, signalingAddr, true
}
