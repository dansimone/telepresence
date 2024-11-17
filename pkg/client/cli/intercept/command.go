package intercept

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Command struct {
	EnvFlags      env.Flags
	DockerFlags   docker.Flags
	MountFlags    mount.Flags
	Name          string // Command[0] || `${Command[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	AgentName     string // --workload || Command[0] // only valid if !localOnly
	Port          string // --port
	ServiceName   string // --service
	ContainerName string // --container
	Address       string // --address

	Replace bool // whether --replace was passed

	ToPod []string // --to-pod

	Cmdline []string // Command[1:]

	Mechanism       string // --mechanism tcp
	MechanismArgs   []string
	ExtendedInfo    []byte
	WaitMessage     string // Message printed when a containerized intercept handler is started and waiting for an interrupt
	FormattedOutput bool
	DetailedOutput  bool
	Silent          bool
}

func (c *Command) AddFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringVarP(&c.AgentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flagSet.StringVarP(&c.Port, "port", "p", "", ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run and a daemon that doesn't run in docker', use <local port>:<container port> or `+
		`<local port>:<container port>:<svcPortIdentifier>.`,
	)

	flagSet.StringVar(&c.Address, "address", "127.0.0.1", ``+
		`Local address to forward to, Only accepts IP address as a value. `+
		`e.g. '--address 10.0.0.2'`,
	)

	flagSet.StringVar(&c.ServiceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flagSet.StringVar(&c.ContainerName, "container", "",
		"Name of container that provides the environment and mounts for the intercept. Defaults to the container matching the targetPort")

	flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet)
	c.DockerFlags.AddFlags(flagSet, "intercepted")

	flagSet.StringP("namespace", "n", "", "If present, the namespace scope for this CLI request")

	flagSet.StringVar(&c.Mechanism, "mechanism", "tcp", "Which extension `mechanism` to use")

	flagSet.StringVar(&c.WaitMessage, "wait-message", "", "Message to print when intercept handler has started")

	flagSet.BoolVar(&c.DetailedOutput, "detailed-output", false,
		`Provide very detailed info about the intercept when used together with --output=json or --output=yaml'`)

	flagSet.BoolVarP(&c.Replace, "replace", "", false,
		`Indicates if the traffic-agent should replace application containers in workload pods. `+
			`The default behavior is for the agent sidecar to be installed alongside existing containers.`)
}

func (c *Command) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with intercept must come after options")
	}
	c.Name = positional[0]
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)

	// Actually intercepting something
	if c.AgentName == "" {
		c.AgentName = c.Name
	}
	if c.Port == "" {
		c.Port = strconv.Itoa(client.GetConfig(cmd.Context()).Intercept().DefaultPort)
	}
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
	_, err := NewState(c, c.MountFlags.ValidateConnected(ctx)).Run(ctx)
	return err
}

func ValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		// Not completing the name of the workload
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := connect.InitCommand(cmd); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	req := connector.ListRequest{
		Filter: connector.ListRequest_INTERCEPTABLE,
	}
	nf := cmd.Flag("namespace")
	if nf.Changed {
		req.Namespace = nf.Value.String()
	}
	ctx := cmd.Context()

	// Trace level is used here, because we generally don't want to log expansion attempts
	// in the cli.log
	dlog.Tracef(ctx, "ns = %s, toComplete = %s, args = %v", req.Namespace, toComplete, args)
	r, err := daemon.GetUserClient(ctx).List(ctx, &req)
	if err != nil {
		dlog.Debugf(ctx, "unable to get list of interceptable workloads: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}

	list := make([]string, 0)
	for _, w := range r.Workloads {
		// only suggest strings that start with the string were autocompleting
		if strings.HasPrefix(w.Name, toComplete) {
			list = append(list, w.Name)
		}
	}

	// TODO(raphaelreyna): This list can be quite large (in the double digits of MB).
	// There probably exists a number that would be a good cutoff limit.

	return list, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
