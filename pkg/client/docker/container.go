package docker

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func StopContainer(ctx context.Context, nameOrID string) error {
	cli, err := GetClient(ctx)
	if err == nil {
		opts := container.StopOptions{}
		timeout := client.GetConfig(ctx).Timeouts().Get(client.TimeoutContainerShutdown)
		if timeout > 0 {
			secs := int(timeout / time.Second)
			opts.Timeout = &secs
		}
		err = cli.ContainerStop(ctx, nameOrID, opts)
	}
	return err
}
