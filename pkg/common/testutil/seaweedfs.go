package testutil

import (
	"context"
	"fmt"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	seaweedFSImage  = "chrislusf/seaweedfs:4.33"
	seaweedFSS3Port = "8333/tcp"
)

// NewSeaweedFSContainer starts a single-node SeaweedFS container with its S3 gateway enabled and returns the
// gateway's endpoint URL plus a cleanup function. The gateway runs with no configured identities, so it accepts any
// credentials; callers create the buckets they need through the S3 API (and should retry briefly — the gateway port
// opens slightly before the volume layer is ready to serve writes).
func NewSeaweedFSContainer(ctx context.Context) (string, func(), error) {
	req := testcontainers.ContainerRequest{
		Image:        seaweedFSImage,
		ExposedPorts: []string{seaweedFSS3Port},
		Cmd:          []string{"server", "-s3"},
		WaitingFor:   wait.ForListeningPort(seaweedFSS3Port),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("starting seaweedfs container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("getting container host: %w", err)
	}

	mappedPort, err := container.MappedPort(ctx, seaweedFSS3Port)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("getting mapped port: %w", err)
	}

	endpoint := fmt.Sprintf("http://%s:%s", host, mappedPort.Port())

	cleanup := func() {
		_ = container.Terminate(context.Background())
	}

	return endpoint, cleanup, nil
}
