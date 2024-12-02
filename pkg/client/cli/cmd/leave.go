package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func leave() *cobra.Command {
	var containerName string
	cmd := &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			return removeIngestOrIntercept(cmd.Context(), strings.TrimSpace(args[0]), containerName)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			shellCompDir := cobra.ShellCompDirectiveNoFileComp
			if len(args) != 0 {
				return nil, shellCompDir
			}
			if err := connect.InitCommand(cmd); err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			ctx := cmd.Context()
			userD := daemon.GetUserClient(ctx)
			resp, err := userD.List(ctx, &connector.ListRequest{Filter: connector.ListRequest_INGESTS})
			if err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			if len(resp.Workloads) == 0 {
				return nil, shellCompDir
			}

			var completions []string
			for _, wl := range resp.Workloads {
				for _, ii := range wl.InterceptInfos {
					name := ii.Spec.Name
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
				for _, ig := range wl.IngestInfos {
					name := ig.Workload
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
			}
			return completions, shellCompDir
		},
	}
	cmd.Flags().StringVarP(&containerName, "container", "c", "", "Container name (only relevant for ingest)")
	return cmd
}

func removeIngestOrIntercept(ctx context.Context, name, container string) error {
	userD := daemon.GetUserClient(ctx)

	var ic *manager.InterceptInfo
	var ig *connector.IngestInfo
	var env map[string]string
	var err error
	if container == "" {
		ic, err = userD.GetIntercept(ctx, &manager.GetInterceptRequest{Name: name})
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
	}

	if ic == nil {
		ig, err = userD.GetIngest(ctx, &connector.IngestIdentifier{
			WorkloadName:  name,
			ContainerName: container,
		})
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return err
			}
			// User probably misspelled the name of the intercept/ingest
			return errcat.User.Newf("Intercept or ingest named %q not found", name)
		}
		env = ig.Environment
	} else {
		env = ic.Environment
	}

	handlerContainer, stopContainer := env["TELEPRESENCE_HANDLER_CONTAINER_NAME"]
	if stopContainer {
		// Stop the handler's container. The daemon is most likely running in another
		// container, and won't be able to.
		err = docker.StopContainer(docker.EnableClient(ctx), handlerContainer)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}

	if ic != nil {
		err = intercept.Result(userD.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: name}))
	} else if ig != nil {
		_, err = userD.LeaveIngest(ctx, &connector.IngestIdentifier{
			WorkloadName:  ig.Workload,
			ContainerName: ig.Container,
		})
	}
	if err != nil {
		if stopContainer && strings.Contains(err.Error(), fmt.Sprintf("%q not found", name)) {
			// race condition between stopping the handler (which causes the ingest/intercept to leave) and this call
			err = nil
		}
	}
	return err
}
