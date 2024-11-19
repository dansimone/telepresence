package ingest

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Command struct {
	EnvFlags        env.Flags
	DockerFlags     docker.Flags
	MountFlags      mount.Flags
	WorkloadName    string // --workload || Command[0] // only valid if !localOnly
	ContainerName   string // --container
	WaitMessage     string
	ToPod           []string // --to-pod
	Cmdline         []string
	FormattedOutput bool
}

func (c *Command) AddFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringVarP(&c.ContainerName, "container", "c", "", "Name of container that provides the environment and mounts for the ingest")

	flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward from the ingested pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the ingested pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet)
	c.DockerFlags.AddFlags(flagSet, "ingested")
	flagSet.StringVar(&c.WaitMessage, "wait-message", "", "Message to print when ingest handler has started")
}

func (c *Command) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with ingest must come after options")
	}
	c.WorkloadName = positional[0]
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)
	if err := c.MountFlags.Validate(cmd); err != nil {
		return err
	}
	if c.DockerFlags.Mount != "" && !c.MountFlags.Enabled {
		return errors.New("--docker-mount cannot be used with --mount=false")
	}
	return c.DockerFlags.Validate(c.Cmdline)
}

func (c *Command) Run(cmd *cobra.Command, positional []string) error {
	if err := c.Validate(cmd, positional); err != nil {
		return err
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	ctx := dos.WithStdio(cmd.Context(), cmd)
	return NewState(c, c.MountFlags.ValidateConnected(ctx)).Run(ctx)
}
