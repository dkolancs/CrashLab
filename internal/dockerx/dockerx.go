package dockerx

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/client"
)

// New returns a Docker client pointed at the local daemon.
func New() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("cannot reach the Docker daemon: %w", err)
	}
	return cli, nil
}
