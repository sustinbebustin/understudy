package kinesis

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindMonitoring binds Enable and DisableEnhancedMonitoring: the metrics
// are stored and reported by DescribeStream(Summary), and nothing is
// published (contract 3.5).
func (s *Service) bindMonitoring(b kinesisapi.Binder) {
	cov := registry.StoredOnly{Note: "shard-level metrics are stored and reported on the stream; no metrics are published to CloudWatch"}
	b.EnableEnhancedMonitoring(s.enableMonitoring, cov)
	b.DisableEnhancedMonitoring(s.disableMonitoring, cov)
}

func (s *Service) enableMonitoring(_ context.Context, c *kernel.Call, in *kinesisapi.EnableEnhancedMonitoringInput) (*kinesisapi.EnhancedMonitoringOutput, *awserr.Error) {
	return s.setMonitoring(c, in.StreamName, in.StreamARN, in.ShardLevelMetrics, true)
}

func (s *Service) disableMonitoring(_ context.Context, c *kernel.Call, in *kinesisapi.DisableEnhancedMonitoringInput) (*kinesisapi.EnhancedMonitoringOutput, *awserr.Error) {
	return s.setMonitoring(c, in.StreamName, in.StreamARN, in.ShardLevelMetrics, false)
}

// setMonitoring adds (on) or removes the named metrics, ALL standing for
// the seven others (model MetricsName, kinesis.md 4.4), and reports the
// metrics before and after in enum order.
func (s *Service) setMonitoring(c *kernel.Call, name, arn *string, names []kinesisapi.MetricsName, on bool) (*kinesisapi.EnhancedMonitoringOutput, *awserr.Error) {
	r, err := streamOf(c, name, arn)
	if err != nil {
		return nil, err
	}
	named := expandMetrics(names)
	// before and after are copies: the response is encoded after the lock
	// is released, so it never aliases state (ADR 0005).
	var before, after []kinesisapi.MetricsName
	err = s.updateStream(c, r, func(str *Stream, _ clock.Instant) *awserr.Error {
		before = append([]kinesisapi.MetricsName{}, str.Metrics...)
		after = make([]kinesisapi.MetricsName, 0, len(named))
		for _, m := range metricNames() {
			had, asked := slices.Contains(before, m), slices.Contains(named, m)
			if (on && (had || asked)) || (!on && had && !asked) {
				after = append(after, m)
			}
		}
		str.Metrics = slices.Clone(after)
		return nil
	})
	if err != nil {
		return nil, err
	}
	stream := streamARN(c.Scope(), r.name).String()
	return &kinesisapi.EnhancedMonitoringOutput{
		StreamName:               new(r.name),
		StreamARN:                &stream,
		CurrentShardLevelMetrics: before,
		DesiredShardLevelMetrics: after,
	}, nil
}

// metricNames are the shard-level metrics in enum order, without ALL.
func metricNames() []kinesisapi.MetricsName {
	return slices.DeleteFunc(kinesisapi.MetricsName("").Values(), func(m kinesisapi.MetricsName) bool { return m == kinesisapi.MetricsNameAll })
}

// expandMetrics replaces ALL with the seven names it stands for.
func expandMetrics(names []kinesisapi.MetricsName) []kinesisapi.MetricsName {
	if slices.Contains(names, kinesisapi.MetricsNameAll) {
		return metricNames()
	}
	return names
}
