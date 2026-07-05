package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

// NetworkInfo represents information about a Docker network.
type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Containers []ContainerInfo
}

// ContainerInfo represents a container connected to a network.
type ContainerInfo struct {
	ID        string
	Name      string
	IPv4Addr  string
	IPv6Addr  string
}

// NetworkManager handles Docker network discovery and manipulation.
type NetworkManager struct {
	cli *client.Client
}

// NewNetworkManager creates a new Docker network manager.
func NewNetworkManager() (*NetworkManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify Docker is accessible
	ctx := context.Background()
	_, err = cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		return nil, fmt.Errorf("Docker daemon not accessible: %w", err)
	}

	log.Info().Msg("Connected to Docker daemon")
	return &NetworkManager{cli: cli}, nil
}

// ListNetworks returns all Docker networks.
func (m *NetworkManager) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	result, err := m.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	networks := make([]NetworkInfo, 0, len(result.Items))
	for _, net := range result.Items {
		info := NetworkInfo{
			ID:     net.ID,
			Name:   net.Name,
			Driver: net.Driver,
		}
		networks = append(networks, info)
	}

	return networks, nil
}

// FindNetwork finds a Docker network by name.
func (m *NetworkManager) FindNetwork(ctx context.Context, name string) (*NetworkInfo, error) {
	networks, err := m.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}

	for _, net := range networks {
		if net.Name == name {
			return &net, nil
		}
	}

	return nil, fmt.Errorf("network '%s' not found", name)
}

// InspectNetwork returns detailed information about a network.
func (m *NetworkManager) InspectNetwork(ctx context.Context, networkID string) (*NetworkInfo, error) {
	result, err := m.cli.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect network %s: %w", networkID, err)
	}

	net := result.Network
	info := &NetworkInfo{
		ID:     net.ID,
		Name:   net.Name,
		Driver: net.Driver,
	}

	for containerID, endpoint := range net.Containers {
		info.Containers = append(info.Containers, ContainerInfo{
			ID:       containerID,
			Name:     endpoint.Name,
			IPv4Addr: endpoint.IPv4Address.String(),
			IPv6Addr: endpoint.IPv6Address.String(),
		})
	}

	log.Debug().
		Str("network", net.Name).
		Int("containers", len(info.Containers)).
		Msg("Network inspected")

	return info, nil
}

// Close closes the Docker client.
func (m *NetworkManager) Close() error {
	return m.cli.Close()
}
