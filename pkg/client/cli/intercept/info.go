package intercept

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type Ingress struct {
	Host   string `json:"host,omitempty"    yaml:"host,omitempty"`
	Port   int32  `json:"port,omitempty"    yaml:"port,omitempty"`
	UseTLS bool   `json:"use_tls,omitempty" yaml:"use_tls,omitempty"`
	L5Host string `json:"l5host,omitempty"  yaml:"l5host,omitempty"`
}

type Info struct {
	ID            string            `json:"id,omitempty"              yaml:"id,omitempty"`
	Name          string            `json:"name,omitempty"            yaml:"name,omitempty"`
	Disposition   string            `json:"disposition,omitempty"     yaml:"disposition,omitempty"`
	Message       string            `json:"message,omitempty"         yaml:"message,omitempty"`
	WorkloadKind  string            `json:"workload_kind,omitempty"   yaml:"workload_kind,omitempty"`
	TargetHost    string            `json:"target_host,omitempty"     yaml:"target_host,omitempty"`
	TargetPort    int32             `json:"target_port,omitempty"     yaml:"target_port,omitempty"`
	ServiceUID    string            `json:"service_uid,omitempty"     yaml:"service_uid,omitempty"`
	ServicePortID string            `json:"service_port_id,omitempty" yaml:"service_port_id,omitempty"` // ServicePortID is deprecated. Use PortID
	PortID        string            `json:"port_id,omitempty"         yaml:"port_id,omitempty"`
	ContainerPort int32             `json:"container_port,omitempty"  yaml:"container_port,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"     yaml:"environment,omitempty"`
	Mount         *mount.Info       `json:"mount,omitempty"           yaml:"mount,omitempty"`
	FilterDesc    string            `json:"filter_desc,omitempty"     yaml:"filter_desc,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"        yaml:"metadata,omitempty"`
	HttpFilter    []string          `json:"http_filter,omitempty"     yaml:"http_filter,omitempty"`
	Global        bool              `json:"global,omitempty"          yaml:"global,omitempty"`
	PreviewURL    string            `json:"preview_url,omitempty"     yaml:"preview_url,omitempty"`
	Ingress       *Ingress          `json:"ingress,omitempty"         yaml:"ingress,omitempty"`
	PodIP         string            `json:"pod_ip,omitempty"          yaml:"pod_ip,omitempty"`
	debug         bool
}

func NewIngress(ps *manager.PreviewSpec) *Ingress {
	if ps == nil {
		return nil
	}
	ii := ps.Ingress
	if ii == nil {
		return nil
	}
	return &Ingress{
		Host:   ii.Host,
		Port:   ii.Port,
		UseTLS: ii.UseTls,
		L5Host: ii.L5Host,
	}
}

func PreviewURL(pu string) string {
	if !(pu == "" || strings.HasPrefix(pu, "https://") || strings.HasPrefix(pu, "http://")) {
		pu = "https://" + pu
	}
	return pu
}

func NewInfo(ctx context.Context, ii *manager.InterceptInfo, mountError error) *Info {
	spec := ii.Spec
	var m *mount.Info
	if mountError != nil {
		m = &mount.Info{Error: mountError.Error()}
	} else if ii.MountPoint != "" {
		m = mount.NewInfo(ctx, ii.Environment, ii.FtpPort, ii.SftpPort, ii.ClientMountPoint, ii.MountPoint, ii.PodIp, false)
	}
	info := &Info{
		ID:            ii.Id,
		Name:          spec.Name,
		Disposition:   ii.Disposition.String(),
		Message:       ii.Message,
		WorkloadKind:  spec.WorkloadKind,
		TargetHost:    spec.TargetHost,
		TargetPort:    spec.TargetPort,
		Mount:         m,
		ServiceUID:    spec.ServiceUid,
		PortID:        spec.PortIdentifier,
		ContainerPort: spec.ContainerPort,
		PodIP:         ii.PodIp,
		Environment:   ii.Environment,
		FilterDesc:    ii.MechanismArgsDesc,
		Metadata:      ii.Metadata,
		HttpFilter:    spec.MechanismArgs,
		Global:        spec.Mechanism == "tcp",
		PreviewURL:    PreviewURL(ii.PreviewDomain),
		Ingress:       NewIngress(ii.PreviewSpec),
	}
	if spec.ServiceUid != "" {
		// For backward compatibility in JSON output
		info.ServicePortID = info.PortID
	}
	return info
}

func (ii *Info) WriteTo(w io.Writer) (int64, error) {
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Prefix = "   "
	kvf.Add("Intercept name", ii.Name)
	kvf.Add("State", func() string {
		msg := ""
		if manager.InterceptDispositionType_value[ii.Disposition] > int32(manager.InterceptDispositionType_WAITING) {
			msg += "error: "
		}
		msg += ii.Disposition
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}())
	kvf.Add("Workload kind", ii.WorkloadKind)

	if ii.debug {
		kvf.Add("ID", ii.ID)
	}

	kvf.Add(
		"Destination",
		net.JoinHostPort(ii.TargetHost, fmt.Sprintf("%d", ii.TargetPort)),
	)

	if ii.PortID != "" {
		if ii.ServiceUID == "" {
			kvf.Add("Container Port Identifier", ii.PortID)
		} else {
			kvf.Add("Service Port Identifier", ii.PortID)
		}
	}
	if ii.debug {
		m := "http"
		if ii.Global {
			m = "tcp"
		}
		kvf.Add("Mechanism", m)
		kvf.Add("Mechanism Command", fmt.Sprintf("%q", ii.FilterDesc))
		kvf.Add("Metadata", fmt.Sprintf("%q", ii.Metadata))
	}

	if m := ii.Mount; m != nil {
		if m.LocalDir != "" {
			kvf.Add("Volume Mount Point", m.LocalDir)
		} else if m.Error != "" {
			kvf.Add("Volume Mount Error", m.Error)
		}
	}

	kvf.Add("Intercepting", func() string {
		if ii.FilterDesc != "" {
			return ii.FilterDesc
		}
		if ii.Global {
			return `using mechanism "tcp"`
		}
		return fmt.Sprintf("using mechanism=%q with args=%q", "http", ii.HttpFilter)
	}())
	if ii.ServiceUID == "" {
		kvf.Add("Address", iputil.JoinHostPort(ii.PodIP, uint16(ii.ContainerPort)))
	}

	if ii.PreviewURL != "" {
		previewURL := ii.PreviewURL
		// Right now SystemA gives back domains with the leading "https://", but
		// let's not rely on that.
		if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
			previewURL = "https://" + previewURL
		}
		kvf.Add("Preview URL", previewURL)
	}
	if in := ii.Ingress; in != nil {
		kvf.Add("Layer 5 Hostname", in.L5Host)
	}
	return kvf.WriteTo(w)
}
