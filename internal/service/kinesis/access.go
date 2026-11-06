package kinesis

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// declareAccess states what each call of the 21 bound operations needs
// from IAM (spec enforce matrix row 1): its own action on the stream it
// names, by StreamName in the caller's scope or by StreamARN as sent, and
// ListStreams and DescribeLimits, which name no stream, on "*".
// CreateStream adds kinesis:AddTagsToStream only when it carries Tags.
// The streams are read before the handler runs, so an input naming no
// resolvable stream is asked on "*" and the handler answers it as usual.
func declareAccess(b *kernel.Binder) {
	none := kernel.WithoutSecondaryActions()
	named := func(c *kernel.Call, name, arn *string) []aws.ARN {
		if arn != nil {
			return claimedStream(*arn)
		}
		if name != nil {
			return []aws.ARN{streamARN(c.Scope(), *name)}
		}
		return nil
	}
	kernel.Declare(b, kinesisapi.OpCreateStream,
		kernel.WithActions(func(_ *kernel.Call, in *kinesisapi.CreateStreamInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			// "tagging at creation needs kinesis:AddTagsToStream", and
			// kinesis:TagResource "won't work" (kinesis.md 4.1).
			return []registry.IAMAction{{Prefix: "kinesis", Name: "AddTagsToStream"}}
		}),
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.CreateStreamInput) []aws.ARN {
			return named(c, &in.StreamName, nil)
		}))
	kernel.Declare(b, kinesisapi.OpDeleteStream, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.DeleteStreamInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpDescribeStream, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.DescribeStreamInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpDescribeStreamSummary, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.DescribeStreamSummaryInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpListStreams, none)
	kernel.Declare(b, kinesisapi.OpDescribeLimits, none)
	kernel.Declare(b, kinesisapi.OpListShards, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.ListShardsInput) []aws.ARN {
			if in.NextToken != nil && in.StreamName == nil && in.StreamARN == nil {
				if name, ok := claimedListStream(*in.NextToken); ok {
					return []aws.ARN{streamARN(c.Scope(), name)}
				}
				return nil
			}
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpIncreaseStreamRetentionPeriod, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.IncreaseStreamRetentionPeriodInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpDecreaseStreamRetentionPeriod, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.DecreaseStreamRetentionPeriodInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpPutRecord, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.PutRecordInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpPutRecords, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.PutRecordsInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpGetShardIterator, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.GetShardIteratorInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	// GetRecords names its stream in the iterator's payload, read before
	// the handler verifies the token, or by StreamARN when it is sent.
	kernel.Declare(b, kinesisapi.OpGetRecords, none,
		kernel.WithResources(func(_ *kernel.Call, in *kinesisapi.GetRecordsInput) []aws.ARN {
			if in.StreamARN != nil {
				return claimedStream(*in.StreamARN)
			}
			stream, ok := shardlog.ClaimedOpaqueStream([]byte(unquoteIterator(in.ShardIterator)))
			if !ok || stream.Service != "kinesis" {
				return nil
			}
			return []aws.ARN{stream}
		}))
	kernel.Declare(b, kinesisapi.OpEnableEnhancedMonitoring, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.EnableEnhancedMonitoringInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpDisableEnhancedMonitoring, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.DisableEnhancedMonitoringInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpAddTagsToStream, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.AddTagsToStreamInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpRemoveTagsFromStream, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.RemoveTagsFromStreamInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpListTagsForStream, none,
		kernel.WithResources(func(c *kernel.Call, in *kinesisapi.ListTagsForStreamInput) []aws.ARN {
			return named(c, in.StreamName, in.StreamARN)
		}))
	kernel.Declare(b, kinesisapi.OpTagResource, none,
		kernel.WithResources(func(_ *kernel.Call, in *kinesisapi.TagResourceInput) []aws.ARN { return claimedStream(in.ResourceARN) }))
	kernel.Declare(b, kinesisapi.OpUntagResource, none,
		kernel.WithResources(func(_ *kernel.Call, in *kinesisapi.UntagResourceInput) []aws.ARN {
			return claimedStream(in.ResourceARN)
		}))
	kernel.Declare(b, kinesisapi.OpListTagsForResource, none,
		kernel.WithResources(func(_ *kernel.Call, in *kinesisapi.ListTagsForResourceInput) []aws.ARN {
			return claimedStream(in.ResourceARN)
		}))
}

// claimedStream is the stream an ARN names, in whatever scope it names:
// nothing when it is not a stream ARN.
func claimedStream(arn string) []aws.ARN {
	a, ok := parseStreamARN(arn)
	if !ok {
		return nil
	}
	return []aws.ARN{a}
}

// claimedListStream is the stream name a ListShards NextToken's payload
// carries, unverified: resumeListing verifies the token when the handler
// runs.
func claimedListStream(token string) (string, bool) {
	raw, err := base64.StdEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) <= sha256.Size {
		return "", false
	}
	var t listShardsToken
	if json.Unmarshal(raw[:len(raw)-sha256.Size], &t) != nil || t.Listing.Stream == "" {
		return "", false
	}
	return t.Listing.Stream, true
}
