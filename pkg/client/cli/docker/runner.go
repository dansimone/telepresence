package docker

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/docker/docker/errdefs"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/spinner"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type Runner struct {
	Flags
	ContainerName string
	Environment   map[string]string
	Mount         *mount.Info
	Ports         []string
}

func (s *Runner) Run(ctx context.Context, waitMessage string, args ...string) error {
	ud := daemon.GetUserClient(ctx)
	file, err := os.CreateTemp("", "tel-*.env")
	if err != nil {
		return fmt.Errorf("failed to create temporary environment file. %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil {
			dlog.Errorf(ctx, "failed to remove temporary environment file %q: %v", file.Name(), err)
		}
	}()

	if err = env.SyntaxDocker.WriteToFileAndClose(file, s.Environment); err != nil {
		return err
	}
	envFile := file.Name()

	// Ensure that the intercept handler is stopped properly if the daemon quits
	procCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := daemon.CancelWhenRmFromCache(procCtx, cancel, ud.DaemonID().InfoFileName()); err != nil {
			dlog.Error(ctx)
		}
	}()

	errRdr, errWrt := io.Pipe()
	procCtx = dos.WithStderr(procCtx, errWrt)
	outRdr, outWrt := io.Pipe()
	procCtx = dos.WithStdout(procCtx, outWrt)

	spin := spinner.New(ctx, "container "+s.ContainerName)
	spin.Message("starting")
	w := s.start(procCtx, s.ContainerName, envFile, args)
	if w.err == nil {
		w.err = ud.AddHandler(ctx, s.Environment["TELEPRESENCE_INTERCEPT_ID"], w.cmd, w.name)
		spin.Message("started")
		spin.DoneMsg(waitMessage)
		if waitMessage != "" && spin.IsNoOp() {
			ioutil.Println(dos.Stdout(ctx), waitMessage)
		}
	} else {
		_ = spin.Error(w.err)
	}
	go func() {
		_, _ = io.Copy(dos.Stdout(ctx), outRdr)
	}()
	go func() {
		_, _ = io.Copy(dos.Stderr(ctx), errRdr)
	}()

	if err := w.wait(procCtx); err != nil {
		return spin.Error(err)
	}
	spin.Done()
	return nil
}

func (s *Runner) start(ctx context.Context, name, envFile string, args []string) *waiter {
	ourArgs := []string{
		"run",
		"--env-file", envFile,
	}
	w := &waiter{name: name}

	if s.Debug {
		ourArgs = append(ourArgs, "--security-opt", "apparmor=unconfined", "--cap-add", "SYS_PTRACE")
	}

	// "--rm" is mandatory when using --docker-run, because without it, the name cannot be reused and
	// the volumes cannot be removed.
	_, set, err := flags.GetUnparsedBoolean(args, "--rm")
	if err != nil {
		w.err = err
		return w
	}
	if !set {
		ourArgs = append(ourArgs, "--rm")
	}

	ud := daemon.GetUserClient(ctx)
	if !ud.Containerized() {
		// The process is containerized but the user daemon runs on the host
		ourArgs = append(ourArgs, "--dns-search", "tel2-search")
		for _, p := range s.Ports {
			ourArgs = append(ourArgs, "-p", p)
		}
		if m := s.Mount; m != nil {
			for _, mv := range m.Mounts {
				ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s/%s:%s", m.LocalDir, mv, mv))
			}
		}
	} else {
		daemonName := ud.DaemonID().ContainerName()
		ourArgs = append(ourArgs, "--network", "container:"+daemonName)

		if m := s.Mount; m != nil {
			pluginName, err := docker.EnsureVolumePlugin(ctx)
			if err != nil {
				ioutil.Printf(output.Err(ctx), "Remote mount disabled: %s\n", err)
			} else {
				container := s.Environment["TELEPRESENCE_CONTAINER"]
				dlog.Infof(ctx, "Mounting %s from container %s", m.RemoteDir, container)
				w.volumes, w.err = docker.StartVolumeMounts(ctx, pluginName, daemonName, container, m.Port, m.Mounts, nil, m.ReadOnly)
				if w.err != nil {
					dlog.Error(ctx, w.err)
					return w
				}
				for i, vol := range w.volumes {
					ro := ""
					if m.ReadOnly {
						ro = ":ro"
					}
					ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s%s", vol, m.Mounts[i], ro))
				}
			}
		}
	}

	args = append(ourArgs, args...)
	w.cmd, w.err = proc.Start(context.WithoutCancel(ctx), nil, "docker", args...)
	return w
}

type waiter struct {
	cmd *dexec.Cmd

	// err is the error (if any) produced by the run
	err error

	// name of container to stop when the run ends
	name string

	// volume mounts to stop when the run ends
	volumes []string
}

func (w *waiter) wait(ctx context.Context) error {
	if len(w.volumes) > 0 {
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			docker.StopVolumeMounts(ctx, w.volumes)
			cancel()
		}()
	}

	if w.err != nil {
		dlog.Error(ctx, w.err)
		return errcat.NoDaemonLogs.New(w.err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, proc.SignalsToForward...)
	defer func() {
		signal.Stop(sigCh)
	}()

	killTimer := time.AfterFunc(math.MaxInt64, func() {
		_ = w.cmd.Process.Kill()
	})
	defer killTimer.Stop()

	var signalled atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-sigCh:
		}
		signalled.Store(true)
		// Kill the docker run after a grace period in case it isn't stopped
		killTimer.Reset(2 * time.Second)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := docker.StopContainer(docker.EnableClient(ctx), w.name); err != nil {
			if !errdefs.IsNotFound(err) {
				dlog.Error(ctx, err)
			}
		}
	}()

	err := w.cmd.Wait()
	if err != nil {
		if signalled.Load() {
			// Errors caused by context or signal termination don't count.
			err = nil
		} else {
			err = errcat.NoDaemonLogs.New(err)
		}
	}
	return err
}
