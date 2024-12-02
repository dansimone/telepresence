package remotefs

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type sftpMounter struct {
	sync.Mutex
	iceptWG *sync.WaitGroup
	podWG   *sync.WaitGroup
}

func NewSFTPMounter(iceptWG, podWG *sync.WaitGroup) Mounter {
	return &sftpMounter{iceptWG: iceptWG, podWG: podWG}
}

func (m *sftpMounter) Start(ctx context.Context, workload, container, clientMountPoint, mountPoint string, podIP net.IP, port uint16, ro bool) error {
	ctx = dgroup.WithGoroutineName(ctx, iputil.JoinIpPort(podIP, port))

	// The mount is terminated and restarted when the intercept pod changes, so we
	// must set up a wait/done pair here to ensure that this happens synchronously
	m.podWG.Add(1)
	m.iceptWG.Add(1)
	go func() {
		defer m.iceptWG.Done()
		defer m.podWG.Done()

		// Be really sure that the following doesn't happen in parallel using multiple
		// pods for the same intercept. One must die before the next is created.
		m.Lock()
		defer m.Unlock()

		dlog.Infof(ctx, "Mounting SFTP file system for container %s[%s] (pod %s) at %q", workload, container, podIP, clientMountPoint)
		defer func() {
			dlog.Infof(ctx, "Unmounting SFTP file system for container %s[%s] (pod %s) at %q", workload, container, podIP, clientMountPoint)
			time.Sleep(time.Second)

			// sshfs sometimes leave the mount point in a bad state. This will clean it up
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			umount := proc.CommandContext(ctx, "fusermount", "-uz", clientMountPoint)
			umount.DisableLogging = true
			_ = umount.Run()
		}()

		// Retry mount in case it gets disconnected
		bc := backoff.WithContext(backoff.NewConstantBackOff(3*time.Second), ctx)
		err := backoff.Retry(func() error {
			sshfsArgs := []string{
				"-F", "none", // don't load the user's config file
				"-f", // foreground operation

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",

				// mount directives
				"-o", "follow_symlinks",
				"-o", "allow_root", // needed to make --docker-run work as docker runs as root
			}
			if ro {
				sshfsArgs = append(sshfsArgs, "-o", "ro")
			}

			useIPv6 := len(podIP) == 16
			if useIPv6 {
				// Must use stdin/stdout because sshfs is not capable of connecting with IPv6
				sshfsArgs = append(sshfsArgs,
					"-o", "slave",
					fmt.Sprintf("localhost:%s", mountPoint),
					clientMountPoint, // where to mount it
				)
			} else {
				sshfsArgs = append(sshfsArgs,
					"-o", fmt.Sprintf("directport=%d", port),
					fmt.Sprintf("%s:%s", podIP.String(), mountPoint), // what to mount
					clientMountPoint, // where to mount it
				)
			}

			exe := "sshfs"
			if runtime.GOOS == "windows" {
				// Use sshfs-win to launch the sshfs
				sshfsArgs = append([]string{"cmd", "-ouid=-1", "-ogid=-1"}, sshfsArgs...)
				exe = "sshfs-win"
			}
			var err error
			if useIPv6 {
				var conn net.Conn
				if conn, err = net.Dial("tcp6", iputil.JoinIpPort(podIP, port)); err == nil {
					defer conn.Close()
					err = dpipe.DPipe(ctx, conn, exe, sshfsArgs...)
				}
			} else {
				err = proc.Run(ctx, nil, exe, sshfsArgs...)
			}
			return err
		}, bc)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	return nil
}
