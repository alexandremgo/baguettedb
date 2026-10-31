package testutil

import (
	"context"
	"fmt"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	postgresImage    = "postgres:18-alpine"
	postgresUser     = "test"
	postgresPassword = "test"
	postgresDB       = "test"
	postgresPort     = "5432/tcp"
)

// NewPostgresContainer starts a Postgres container and returns the DSN and a cleanup function.
// The caller is responsible for running migrations and calling cleanup when done.
func NewPostgresContainer(ctx context.Context) (string, func(), error) {
	req := testcontainers.ContainerRequest{
		Image:        postgresImage,
		ExposedPorts: []string{postgresPort},
		Env: map[string]string{
			"POSTGRES_USER":     postgresUser,
			"POSTGRES_PASSWORD": postgresPassword,
			"POSTGRES_DB":       postgresDB,
		},
		// The image initializes the database with a throwaway server before starting the real one, which opens its port
		// before it accepts connections: only the real server's ready line, the second, means connections succeed.
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(postgresPort),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("starting postgres container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("getting container host: %w", err)
	}

	mappedPort, err := container.MappedPort(ctx, postgresPort)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("getting mapped port: %w", err)
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		postgresUser, postgresPassword, host, mappedPort.Port(), postgresDB)

	cleanup := func() {
		_ = container.Terminate(context.Background())
	}

	return dsn, cleanup, nil
}
