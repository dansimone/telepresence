package daemon

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/blang/semver/v4"
	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

type UserClient interface {
	connector.ConnectorClient
	io.Closer
	Conn() *grpc.ClientConn
	Containerized() bool
	DaemonPort() int
	DaemonID() *Identifier
	Executable() string
	Name() string
	Semver() semver.Version
	SetDaemonID(*Identifier)
	AddHandler(ctx context.Context, id string, cmd *dexec.Cmd, containerName string) error
}

type userClient struct {
	connector.ConnectorClient
	conn       *grpc.ClientConn
	daemonID   *Identifier
	version    semver.Version
	executable string
	name       string
}

var NewUserClientFunc = NewUserClient //nolint:gochecknoglobals // extension point

func NewUserClient(conn *grpc.ClientConn, daemonID *Identifier, version semver.Version, name string, executable string) UserClient {
	return &userClient{ConnectorClient: connector.NewConnectorClient(conn), conn: conn, daemonID: daemonID, version: version, name: name, executable: executable}
}

type Session struct {
	UserClient
	Info    *connector.ConnectInfo
	Started bool
}

type userDaemonKey struct{}

func GetUserClient(ctx context.Context) UserClient {
	if ud, ok := ctx.Value(userDaemonKey{}).(UserClient); ok {
		return ud
	}
	return nil
}

func WithUserClient(ctx context.Context, ud UserClient) context.Context {
	return context.WithValue(ctx, userDaemonKey{}, ud)
}

type sessionKey struct{}

func GetSession(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return s
	}
	return nil
}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func (u *userClient) Close() error {
	return u.conn.Close()
}

func (u *userClient) Conn() *grpc.ClientConn {
	return u.conn
}

func (u *userClient) Containerized() bool {
	return u.daemonID.Containerized
}

func (u *userClient) DaemonID() *Identifier {
	return u.daemonID
}

func (u *userClient) Executable() string {
	return u.executable
}

func (u *userClient) Name() string {
	return u.name
}

func (u *userClient) Semver() semver.Version {
	return u.version
}

func (u *userClient) DaemonPort() int {
	if u.daemonID.Containerized {
		addr := u.conn.Target()
		if lc := strings.LastIndexByte(addr, ':'); lc >= 0 {
			if port, err := strconv.Atoi(addr[lc+1:]); err == nil {
				return port
			}
		}
	}
	return -1
}

func (u *userClient) SetDaemonID(daemonID *Identifier) {
	u.daemonID = daemonID
}

func (u *userClient) AddHandler(ctx context.Context, id string, cmd *dexec.Cmd, containerName string) error {
	// setup cleanup for the handler process
	ior := connector.Interceptor{
		InterceptId:   id,
		Pid:           int32(cmd.Process.Pid),
		ContainerName: containerName,
	}

	// Send info about the pid and intercept id to the traffic-manager so that it kills
	// the process if it receives a leave of quit call.
	if _, err := u.AddInterceptor(ctx, &ior); err != nil {
		if grpcStatus.Code(err) == grpcCodes.Canceled {
			// Deactivation was caused by a disconnect
			err = nil
		} else {
			dlog.Errorf(ctx, "error adding process with pid %d as interceptor: %v", ior.Pid, err)
		}
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}
