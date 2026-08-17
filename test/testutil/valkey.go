package testutil

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartValkey launches a valkey/valkey:8-alpine container and returns its
// host:port address and a teardown function that must be called
// (typically via defer in TestMain) to terminate the container.
func StartValkey(ctx context.Context) (addr string, teardown func(), err error) {
	req := testcontainers.ContainerRequest{
		Image:        "valkey/valkey:8-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start valkey container: %w", err)
	}
	// See the equivalent comment in testutil/postgres.go: teardown
	// deliberately uses a fresh background context, not ctx.
	teardown = func() { //nolint:contextcheck // see comment above
		if termErr := container.Terminate(context.Background()); termErr != nil {
			fmt.Fprintln(os.Stderr, "testutil: terminate valkey container:", termErr)
		}
	}

	host, err := container.Host(ctx)
	if err != nil {
		teardown()
		return "", nil, fmt.Errorf("get valkey host: %w", err)
	}
	port, err := container.MappedPort(ctx, "6379")
	if err != nil {
		teardown()
		return "", nil, fmt.Errorf("get valkey port: %w", err)
	}

	return fmt.Sprintf("%s:%s", host, port.Port()), teardown, nil
}
