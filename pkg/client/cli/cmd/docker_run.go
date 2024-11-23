package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func dockerRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker-run",
		Short: "Docker run with daemon network",
		Args:  cobra.ArbitraryArgs,
		Annotations: map[string]string{
			ann.Session: ann.Optional,
		},
		RunE:                  runDockerRunCLI,
		SilenceErrors:         true,
		SilenceUsage:          true,
		DisableFlagParsing:    true,
		DisableFlagsInUseLine: true,
		DisableSuggestions:    true,
	}
	return cmd
}

func findAndParseFlag(flags *pflag.FlagSet, flagName string, args []string) ([]string, error) {
	if i := slices.Index(args, "--"+flagName); i >= 0 && i+1 < len(args) {
		if err := flags.Parse(args[i : i+2]); err != nil {
			return nil, err
		}
		args = slices.Delete(args, i, i+2)
	} else if i = slices.IndexFunc(args, func(s string) bool { return strings.HasPrefix(s, "--"+flagName+"=") }); i >= 0 {
		if err := flags.Parse(args[i : i+1]); err != nil {
			return nil, err
		}
		args = slices.Delete(args, i, i+1)
	}
	return args, nil
}

const networkFlag = "network"

func parseFlags(cmd *cobra.Command, args []string) (*pflag.FlagSet, []string, error) {
	// The command has all flag parsing disabled, but we must check for the global flags. Luckily, these flags do not conflict with
	// the docker run flags.
	flags := cmd.Flags()
	flags.String(networkFlag, "", "")
	var err error
	args, err = findAndParseFlag(flags, global.FlagUse, args)
	if err != nil {
		return nil, nil, err
	}
	args, err = findAndParseFlag(flags, global.FlagOutput, args)
	if err != nil {
		return nil, nil, err
	}

	// We "trap" any passing of "--network" here, because we pass a "--network container:<daemon>" ourselves. Any
	// network provided by the user must be attached after the container has been started.
	args, err = findAndParseFlag(flags, networkFlag, args)
	if err != nil {
		return nil, nil, err
	}
	return flags, args, nil
}

func runDockerRunCLI(cmd *cobra.Command, args []string) error {
	return errcat.NoDaemonLogs.New(runDockerRun(cmd, args))
}

func runDockerRun(cmd *cobra.Command, args []string) error {
	flags, args, err := parseFlags(cmd, args)
	if err != nil {
		return err
	}

	const exe = "docker"
	if slices.Contains(args, "--help") {
		return proc.StdCommand(cmd.Context(), exe, slices.Insert(args, 0, "run")...).Run()
	}

	var network string
	if nf := flags.Lookup(networkFlag); nf.Changed {
		network = nf.Value.String()
		if strings.HasPrefix(network, "container:") {
			return errors.New("this command adds the daemon container network. Adding another container network is not possible")
		}
	}

	err = connect.InitCommand(cmd)
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	ud := daemon.GetUserClient(ctx)
	if ud == nil {
		return fmt.Errorf("%s requires a connection", cmd.UseLine())
	}
	if !ud.Containerized() {
		return fmt.Errorf("%s requires that --docker was used when the connection was established", cmd.UseLine())
	}

	cidFileName, err := ioutil.CreateTempName("", "docker-run*.cid")
	if err != nil {
		return err
	}

	daemonName := ud.DaemonID().ContainerName()
	ctx = dos.WithStdio(ctx, cmd)

	cc := proc.StdCommand(ctx, exe, slices.Insert(args, 0, "run", "--cidfile", cidFileName, "--network", "container:"+daemonName)...)
	cc.Stdin = dos.Stdin(ctx)
	cc.Env = dos.Environ(ctx)
	tty := hasOption("tty", 't', args)
	if !tty {
		proc.CreateNewProcessGroup(cc.Cmd)
	}

	defer func() {
		_ = os.Remove(cidFileName)
	}()

	err = cc.Start()
	if err != nil {
		return err
	}

	containerID, err := readContainerID(ctx, cidFileName)
	if err != nil {
		return err
	}

	ctx = docker.EnableClient(ctx)

	var exited, signalled atomic.Bool
	if !tty {
		go cliDocker.EnsureStopContainer(ctx, containerID, &exited, &signalled)
	}

	if network != "" {
		cli, err := docker.GetClient(ctx)
		if err != nil {
			return err
		}
		connected, err := connectDaemon(ctx, cli, network, daemonName)
		if err != nil {
			return err
		}
		if connected {
			defer disconnectDaemon(ctx, cli, network, daemonName)
		}
	}

	err = cc.Wait()
	exited.Store(true)
	if signalled.Load() {
		err = nil
	}
	return err
}

func hasOption(longForm string, shortForm byte, args []string) bool {
	longFlag := "--" + longForm
	return slices.ContainsFunc(args, func(s string) bool {
		return s == longFlag || len(s) >= 2 && s[0] == '-' && s[1] != '-' && strings.IndexByte(s, shortForm) > 0
	})
}

func readContainerID(ctx context.Context, cidFile string) (containerID string, err error) {
	err = backoff.Retry(func() error {
		cid, err := os.ReadFile(cidFile)
		if err != nil {
			return err
		}
		if len(cid) == 0 {
			return exec.ErrNotFound
		}
		containerID = string(cid)
		return nil
	}, backoff.WithContext(backoff.NewConstantBackOff(10*time.Millisecond), ctx))
	return containerID, err
}

// connectDaemonToNetwork attaches the given network to the containerized daemon. It will
// return false if the daemon already had this network attached.
func connectDaemon(ctx context.Context, cli *client.Client, network, daemonName string) (bool, error) {
	dlog.Debugf(ctx, "Connecting network %s to container %s", network, daemonName)
	if err := cli.NetworkConnect(ctx, network, daemonName, nil); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return false, nil
		}
		return false, fmt.Errorf("failed to connect network %s to container %s: %v", network, daemonName, err)
	}
	return true, nil
}

// disconnectDaemon attaches the given network to the containerized daemon. It will
// return false if the daemon already had this network attached.
func disconnectDaemon(ctx context.Context, cli *client.Client, network, daemonName string) {
	dlog.Debugf(ctx, "Disconnecting network %s from container %s", network, daemonName)
	err := cli.NetworkDisconnect(ctx, network, daemonName, false)
	if err != nil {
		dlog.Warnf(ctx, "failed to disconnect network %s from daemon: %v", network, err)
	}
}
