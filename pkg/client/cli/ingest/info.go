package ingest

import (
	"context"
	"io"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type Info struct {
	WorkloadKind string            `json:"workload_kind,omitempty"              yaml:"workload_kind,omitempty"`
	Container    string            `json:"container,omitempty"            yaml:"container,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"     yaml:"environment,omitempty"`
	Mount        *mount.Info       `json:"mount,omitempty"           yaml:"mount,omitempty"`
	PodIP        string            `json:"pod_ip,omitempty"          yaml:"pod_ip,omitempty"`
}

func NewInfo(ctx context.Context, ii *rpc.IngestInfo, mountError error) *Info {
	var m *mount.Info
	if mountError != nil {
		m = &mount.Info{Error: mountError.Error()}
	} else if ii.MountPoint != "" {
		m = mount.NewInfo(ctx, ii.Environment, ii.FtpPort, ii.SftpPort, ii.ClientMountPoint, ii.MountPoint, ii.PodIp, true)
	}
	return &Info{
		WorkloadKind: ii.WorkloadKind,
		Container:    ii.Container,
		Mount:        m,
		PodIP:        ii.PodIp,
		Environment:  ii.Environment,
	}
}

func (ii *Info) WriteTo(w io.Writer) (int64, error) {
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Prefix = "   "
	kvf.Add("Container", ii.Container)
	kvf.Add("Workload kind", ii.WorkloadKind)
	if m := ii.Mount; m != nil {
		if m.LocalDir != "" {
			kvf.Add("Volume Mount Point", m.LocalDir)
		} else if m.Error != "" {
			kvf.Add("Volume Mount Error", m.Error)
		}
	}
	return kvf.WriteTo(w)
}
