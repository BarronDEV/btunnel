package main

import (
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	socketPath := os.Getenv("BTUNNEL_SOCKET_PATH")
	if socketPath == "" {
		socketPath = "/tmp/btunnel/sidecar.sock"
	}

	targetAddr := os.Getenv("BTUNNEL_TARGET_ADDRESS")
	if targetAddr == "" {
		log.Fatal().Msg("BTUNNEL_TARGET_ADDRESS environment variable is required")
	}

	log.Info().
		Str("socket_path", socketPath).
		Str("target_address", targetAddr).
		Msg("BTunnel Sidecar Agent starting")

	// Ensure clean socket startup
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to listen on Unix domain socket")
	}
	defer listener.Close()

	// Adjust permissions so host can access it
	_ = os.Chmod(socketPath, 0777)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Info().Msg("Shutting down sidecar agent...")
		listener.Close()
		_ = os.Remove(socketPath)
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Error().Err(err).Msg("Failed to accept socket connection")
			continue
		}

		go handleConnection(conn, targetAddr)
	}
}

func handleConnection(localConn net.Conn, targetAddr string) {
	defer localConn.Close()

	log.Debug().Str("target", targetAddr).Msg("Dialing target service on Docker network")
	remoteConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Error().Err(err).Str("target", targetAddr).Msg("Failed to connect to target service")
		return
	}
	defer remoteConn.Close()

	// Bidirectional tunnel bridge
	errChan := make(chan error, 2)
	go func() {
		_, err := io.Copy(remoteConn, localConn)
		errChan <- err
	}()
	go func() {
		_, err := io.Copy(localConn, remoteConn)
		errChan <- err
	}()

	err = <-errChan
	if err != nil && err != io.EOF {
		log.Debug().Err(err).Msg("Connection closed with error")
	}
}
