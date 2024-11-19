package intercept

import (
	"context"
	"strings"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
)

func DescribeIntercepts(ctx context.Context, iis []*manager.InterceptInfo, igs []*rpc.IngestInfo, volumeMountsPrevented error, debug bool) string {
	sb := strings.Builder{}
	if len(iis) > 0 {
		sb.WriteString("intercepted")
		for _, ii := range iis {
			sb.WriteByte('\n')
			describeIntercept(ctx, ii, volumeMountsPrevented, debug, &sb)
		}
	}
	if len(igs) > 0 {
		sb.WriteString("ingested")
		for _, ig := range igs {
			sb.WriteByte('\n')
			describeIngest(ctx, ig, volumeMountsPrevented, &sb)
		}
	}
	return sb.String()
}

func describeIntercept(ctx context.Context, ii *manager.InterceptInfo, volumeMountsPrevented error, debug bool, sb *strings.Builder) {
	info := NewInfo(ctx, ii, volumeMountsPrevented)
	info.debug = debug
	_, _ = info.WriteTo(sb)
}

func describeIngest(ctx context.Context, ig *rpc.IngestInfo, volumeMountsPrevented error, sb *strings.Builder) {
	info := ingest.NewInfo(ctx, ig, volumeMountsPrevented)
	_, _ = info.WriteTo(sb)
}
