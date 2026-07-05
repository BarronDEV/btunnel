package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

const (
	// SidecarImage is the Docker image used for sidecar containers.
	SidecarImage = "btunnel-sidecar-agent:latest"

	// SidecarContainerPrefix is the naming prefix for sidecar containers.
	SidecarContainerPrefix = "btunnel-sidecar-"
)

// SidecarManager manages sidecar containers for Docker network isolation.
type SidecarManager struct {
	cli           *client.Client
	networkName   string
	targetAddress string
	containerID   string
	hostSocketDir string
}

// NewSidecarManager creates a new sidecar manager.
func NewSidecarManager(networkName, targetAddress string) (*SidecarManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	hostSocketDir, err := filepath.Abs("./tmp")
	if err != nil {
		hostSocketDir = os.TempDir()
	}

	return &SidecarManager{
		cli:           cli,
		networkName:   networkName,
		targetAddress: targetAddress,
		hostSocketDir: hostSocketDir,
	}, nil
}

// Start launches a sidecar container connected to the target Docker network.
func (s *SidecarManager) Start(ctx context.Context) error {
	containerName := SidecarContainerPrefix + s.networkName

	log.Info().
		Str("network", s.networkName).
		Str("container", containerName).
		Str("target", s.targetAddress).
		Msg("Starting sidecar container")

	// Ensure target socket directory exists
	if err := os.MkdirAll(s.hostSocketDir, 0777); err != nil {
		return fmt.Errorf("failed to create host socket directory: %w", err)
	}

	// Pull sidecar image if needed
	pullResp, err := s.cli.ImagePull(ctx, SidecarImage, client.ImagePullOptions{})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to pull sidecar image, trying local")
	} else {
		io.Copy(io.Discard, pullResp)
		pullResp.Close()
	}

	// Create sidecar container with volume binds and environment variables
	resp, err := s.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Image: SidecarImage,
		Config: &container.Config{
			Env: []string{
				"BTUNNEL_TARGET_ADDRESS=" + s.targetAddress,
				"BTUNNEL_SOCKET_PATH=/tmp/btunnel/sidecar.sock",
			},
			Labels: map[string]string{
				"btunnel.role":    "sidecar",
				"btunnel.network": s.networkName,
			},
		},
		HostConfig: &container.HostConfig{
			AutoRemove: true,
			Binds: []string{
				s.hostSocketDir + ":/tmp/btunnel",
			},
			Resources: container.Resources{
				Memory:   64 * 1024 * 1024,
				NanoCPUs: 500000000,
			},
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				s.networkName: {},
			},
		},
		Name: containerName,
	})
	if err != nil {
		return fmt.Errorf("failed to create sidecar container: %w", err)
	}

	s.containerID = resp.ID

	// Start the container
	if _, err := s.cli.ContainerStart(ctx, s.containerID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start sidecar container: %w", err)
	}

	log.Info().
		Str("container_id", s.containerID[:12]).
		Str("network", s.networkName).
		Msg("Sidecar container started")

	return nil
}

// GetSocketPath returns the path to the Unix domain socket on the host machine.
func (s *SidecarManager) GetSocketPath() string {
	return filepath.Join(s.hostSocketDir, "sidecar.sock")
}

// GetContainerIP returns the IP address of the sidecar container within the target network.
func (s *SidecarManager) GetContainerIP(ctx context.Context) (string, error) {
	result, err := s.cli.ContainerInspect(ctx, s.containerID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to inspect sidecar container: %w", err)
	}

	inspect := result.Container
	networkSettings, ok := inspect.NetworkSettings.Networks[s.networkName]
	if !ok {
		return "", fmt.Errorf("sidecar not connected to network %s", s.networkName)
	}

	return networkSettings.IPAddress.String(), nil
}

// Stop removes the sidecar container.
func (s *SidecarManager) Stop(ctx context.Context) error {
	if s.containerID == "" {
		return nil
	}

	log.Info().
		Str("container_id", s.containerID[:12]).
		Msg("Stopping sidecar container")

	timeout := 5
	_, err := s.cli.ContainerStop(ctx, s.containerID, client.ContainerStopOptions{
		Timeout: &timeout,
	})
	if err != nil {
		if _, rmErr := s.cli.ContainerRemove(ctx, s.containerID, client.ContainerRemoveOptions{Force: true}); rmErr != nil {
			return fmt.Errorf("failed to remove sidecar: %w", rmErr)
		}
	}

	s.containerID = ""
	_ = os.Remove(s.GetSocketPath())
	log.Info().Msg("Sidecar container stopped")
	return nil
}

// Close cleans up the sidecar manager.
func (s *SidecarManager) Close(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		log.Warn().Err(err).Msg("Error stopping sidecar during cleanup")
	}
	return s.cli.Close()
}
