package trafficmgr

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func (pa *podAccess) shouldMount() bool {
	return (pa.ftpPort > 0 || pa.sftpPort > 0) && (pa.localMountPort > 0 || pa.clientMountPoint != "")
}

// startMount starts the mount for the given podAccessKey.
// It assumes that the user has called shouldMount and is sure that something will be started.
func (pa *podAccess) startMount(ctx context.Context, iceptWG, podWG *sync.WaitGroup) {
	var fuseftp rpc.FuseFTPClient
	useFtp := client.GetConfig(ctx).Intercept().UseFtp
	var port int32
	mountCtx := ctx
	if useFtp {
		if pa.ftpPort == 0 {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP is provided by the traffic-agent")
			return
		}
		if pa.localMountPort > 0 {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP can be used with --local-mount-port")
			return
		}
		// The FTP mounter survives multiple starts for the same intercept. It just resets the address
		mountCtx = pa.ctx
		if fuseftp = userd.GetService(ctx).FuseFTPMgr().GetFuseFTPClient(ctx); fuseftp == nil {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but the fuseftp server was unable to start")
			return
		}
		port = pa.ftpPort
	} else {
		if pa.sftpPort == 0 {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using SFTP, but only FTP is provided by the traffic-agent")
			return
		}
		port = pa.sftpPort
	}

	m := *pa.mounter
	if m == nil {
		switch {
		case pa.localMountPort != 0:
			session := userd.GetSession(ctx)
			m = remotefs.NewBridgeMounter(session.SessionInfo().SessionId, session.ManagerClient(), uint16(pa.localMountPort))
		case useFtp:
			m = remotefs.NewFTPMounter(fuseftp, iceptWG)
		default:
			m = remotefs.NewSFTPMounter(iceptWG, podWG)
		}
		*pa.mounter = m
	}
	err := m.Start(mountCtx, pa.workload, pa.container, pa.clientMountPoint, pa.mountPoint, iputil.Parse(pa.podIP), uint16(port), pa.readOnly)
	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}

func (s *session) ensureNoMountConflict(localMountPoint string, localMountPort int32) (err error) {
	if localMountPoint == "" && localMountPort == 0 {
		return nil
	}
	s.currentInterceptsLock.Lock()
	for _, ic := range s.currentIntercepts {
		if localMountPoint != "" && ic.ClientMountPoint == localMountPoint {
			err = status.Error(codes.AlreadyExists, fmt.Sprintf("mount point %s already in use by intercept %s", localMountPoint, ic.Spec.Name))
			break
		}
		if localMountPort != 0 && ic.localMountPort == localMountPort {
			err = status.Error(codes.AlreadyExists, fmt.Sprintf("mount port %d already in use by intercept %s", localMountPort, ic.Spec.Name))
			break
		}
	}
	s.currentInterceptsLock.Unlock()
	return err
}
