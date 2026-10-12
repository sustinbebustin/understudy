package gosdk_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// Research: docs/research/phase1/secrets-logs-events-alarms.md section 3,
// logs-filter-patterns.md, error-matrix.md 5.7.

// createLogGroup creates a log group and deletes it when the test ends.
func createLogGroup(t *testing.T, groupName string, tags map[string]string) {
	t.Helper()
	client := cloudwatchlogs.NewFromConfig(awsCfg)
	_, err := client.CreateLogGroup(t.Context(), &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(groupName), Tags: tags})
	ok(t, err, "CreateLogGroup "+groupName)
	t.Cleanup(func() {
		_, _ = client.DeleteLogGroup(context.Background(), &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(groupName)})
	})
}

// describeGroup returns the log group named exactly groupName.
func describeGroup(t *testing.T, client *cloudwatchlogs.Client, groupName string) types.LogGroup {
	t.Helper()
	out, err := client.DescribeLogGroups(t.Context(), &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(groupName)})
	ok(t, err, "DescribeLogGroups")
	i := slices.IndexFunc(out.LogGroups, func(g types.LogGroup) bool { return aws.ToString(g.LogGroupName) == groupName })
	if i < 0 {
		t.Fatalf("DescribeLogGroups does not list %s", groupName)
	}
	return out.LogGroups[i]
}

// millis is t in epoch milliseconds, the unit of every Logs timestamp.
func millis(t time.Time) int64 { return t.UnixMilli() }

func TestLogsGroupsAndStreams(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := cloudwatchlogs.NewFromConfig(awsCfg)
	acct := account(t)
	group := "/conformance/" + name("group")
	createLogGroup(t, group, map[string]string{"team": "platform"})
	groupArn := "arn:aws:logs:" + region() + ":" + acct + ":log-group:" + group

	t.Run("DescribeLogGroups", func(t *testing.T) {
		g := describeGroup(t, client, group)
		if aws.ToString(g.Arn) != groupArn+":*" || aws.ToString(g.LogGroupArn) != groupArn {
			t.Errorf("arn %q, logGroupArn %q; want %q with :* and %q without", aws.ToString(g.Arn), aws.ToString(g.LogGroupArn), groupArn+":*", groupArn)
		}
		if g.RetentionInDays != nil || g.LogGroupClass != types.LogGroupClassStandard || aws.ToInt32(g.MetricFilterCount) != 0 || g.CreationTime == nil || g.KmsKeyId != nil {
			t.Errorf("group = retention %v, class %q, metricFilterCount %d, creationTime %v, kmsKeyId %v",
				g.RetentionInDays, g.LogGroupClass, aws.ToInt32(g.MetricFilterCount), g.CreationTime, g.KmsKeyId)
		}
		_, err := client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String("/a"), LogGroupNamePattern: aws.String("b")})
		errAs[*types.InvalidParameterException](t, err, "DescribeLogGroups with prefix and pattern")
		wantMessage(t, err, "DescribeLogGroups with prefix and pattern", "LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters.")
	})

	t.Run("CreateLogGroup errors", func(t *testing.T) {
		_, err := client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
		errAs[*types.ResourceAlreadyExistsException](t, err, "CreateLogGroup duplicate")
		wantMessage(t, err, "CreateLogGroup duplicate", "The specified log group already exists")
		wantStatus(t, err, "CreateLogGroup duplicate", 400)
	})

	t.Run("retention", func(t *testing.T) {
		_, err := client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(group), RetentionInDays: aws.Int32(7)})
		ok(t, err, "PutRetentionPolicy 7")
		if g := describeGroup(t, client, group); aws.ToInt32(g.RetentionInDays) != 7 {
			t.Errorf("retentionInDays = %v, want 7", g.RetentionInDays)
		}
		_, err = client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(group), RetentionInDays: aws.Int32(2)})
		errAs[*types.InvalidParameterException](t, err, "PutRetentionPolicy 2 (not an allowed value)")
		_, err = client.DeleteRetentionPolicy(ctx, &cloudwatchlogs.DeleteRetentionPolicyInput{LogGroupName: aws.String(group)})
		ok(t, err, "DeleteRetentionPolicy")
		if g := describeGroup(t, client, group); g.RetentionInDays != nil {
			t.Errorf("retentionInDays after delete = %d, want absent", aws.ToInt32(g.RetentionInDays))
		}
	})

	t.Run("streams", func(t *testing.T) {
		_, err := client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s1")})
		ok(t, err, "CreateLogStream")
		_, err = client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s1")})
		errAs[*types.ResourceAlreadyExistsException](t, err, "CreateLogStream duplicate")
		wantMessage(t, err, "CreateLogStream duplicate", "The specified log stream already exists")
		_, err = client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group + "-missing"), LogStreamName: aws.String("s1")})
		errAs[*types.ResourceNotFoundException](t, err, "CreateLogStream in a missing group")
		wantMessage(t, err, "CreateLogStream in a missing group", "The specified log group does not exist.")
		out, err := client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(group), LogStreamNamePrefix: aws.String("s1")})
		ok(t, err, "DescribeLogStreams")
		if len(out.LogStreams) != 1 {
			t.Fatalf("DescribeLogStreams = %+v, want s1", out.LogStreams)
		}
		s := out.LogStreams[0]
		if aws.ToString(s.Arn) != groupArn+":log-stream:s1" || s.CreationTime == nil || s.FirstEventTimestamp != nil || s.LastEventTimestamp != nil {
			t.Errorf("stream = arn %q, creationTime %v, firstEventTimestamp %v, lastEventTimestamp %v; want %q and no event times",
				aws.ToString(s.Arn), s.CreationTime, s.FirstEventTimestamp, s.LastEventTimestamp, groupArn+":log-stream:s1")
		}
		_, err = client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(group), LogGroupIdentifier: aws.String(groupArn)})
		apiErr(t, err, "DescribeLogStreams with name and identifier", "ValidationException")
		_, err = client.DeleteLogStream(ctx, &cloudwatchlogs.DeleteLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s1")})
		ok(t, err, "DeleteLogStream")
		_, err = client.DeleteLogStream(ctx, &cloudwatchlogs.DeleteLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s1")})
		errAs[*types.ResourceNotFoundException](t, err, "DeleteLogStream again")
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(groupArn)})
		ok(t, err, "ListTagsForResource")
		if out.Tags["team"] != "platform" {
			t.Errorf("tags from CreateLogGroup = %v, want team=platform", out.Tags)
		}
		_, err = client.TagResource(ctx, &cloudwatchlogs.TagResourceInput{ResourceArn: aws.String(groupArn), Tags: map[string]string{"env": "test"}})
		ok(t, err, "TagResource")
		// The legacy by-name tag API is deprecated but still served, and
		// both APIs share one tag set (secrets-logs-events-alarms.md 3.7).
		legacy, err := client.ListTagsLogGroup(ctx, &cloudwatchlogs.ListTagsLogGroupInput{LogGroupName: aws.String(group)}) //nolint:staticcheck // legacy API under test
		ok(t, err, "ListTagsLogGroup")
		if legacy.Tags["env"] != "test" || legacy.Tags["team"] != "platform" {
			t.Errorf("ListTagsLogGroup = %v, want both tags (one tag set for both APIs)", legacy.Tags)
		}
		_, err = client.UntagResource(ctx, &cloudwatchlogs.UntagResourceInput{ResourceArn: aws.String(groupArn), TagKeys: []string{"team"}})
		ok(t, err, "UntagResource")
		out, err = client.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(groupArn)})
		ok(t, err, "ListTagsForResource")
		if _, has := out.Tags["team"]; has || out.Tags["env"] != "test" {
			t.Errorf("tags after untag = %v, want only env=test", out.Tags)
		}
		_, err = client.TagLogGroup(ctx, &cloudwatchlogs.TagLogGroupInput{LogGroupName: aws.String(group), Tags: map[string]string{"legacy": "yes"}}) //nolint:staticcheck // legacy API under test
		ok(t, err, "TagLogGroup")
		_, err = client.UntagLogGroup(ctx, &cloudwatchlogs.UntagLogGroupInput{LogGroupName: aws.String(group), Tags: []string{"env"}}) //nolint:staticcheck // legacy API under test
		ok(t, err, "UntagLogGroup")
		out, err = client.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(groupArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 || out.Tags["legacy"] != "yes" {
			t.Errorf("tags after the legacy tag calls = %v, want only legacy=yes", out.Tags)
		}
	})

	t.Run("deletion protection", func(t *testing.T) {
		g := "/conformance/" + name("protected")
		createLogGroup(t, g, nil)
		if d := describeGroup(t, client, g); aws.ToBool(d.DeletionProtectionEnabled) {
			t.Errorf("a new group has deletion protection enabled")
		}
		_, err := client.PutLogGroupDeletionProtection(ctx, &cloudwatchlogs.PutLogGroupDeletionProtectionInput{LogGroupIdentifier: aws.String(g), DeletionProtectionEnabled: aws.Bool(true)})
		ok(t, err, "PutLogGroupDeletionProtection true")
		if d := describeGroup(t, client, g); !aws.ToBool(d.DeletionProtectionEnabled) {
			t.Errorf("deletionProtectionEnabled after enabling = false")
		}
		_, err = client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(g)})
		if err == nil {
			t.Errorf("DeleteLogGroup of a protected group succeeded")
		}
		_, err = client.PutLogGroupDeletionProtection(ctx, &cloudwatchlogs.PutLogGroupDeletionProtectionInput{LogGroupIdentifier: aws.String(g), DeletionProtectionEnabled: aws.Bool(false)})
		ok(t, err, "PutLogGroupDeletionProtection false")
	})

	t.Run("DeleteLogGroup", func(t *testing.T) {
		g := "/conformance/" + name("gone")
		_, err := client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(g)})
		ok(t, err, "CreateLogGroup")
		_, err = client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(g)})
		ok(t, err, "DeleteLogGroup")
		_, err = client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(g)})
		errAs[*types.ResourceNotFoundException](t, err, "DeleteLogGroup again")
		out, err := client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(g)})
		ok(t, err, "DescribeLogGroups")
		if len(out.LogGroups) != 0 {
			t.Errorf("DescribeLogGroups after delete = %+v", out.LogGroups)
		}
	})
}

func TestLogsEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := cloudwatchlogs.NewFromConfig(awsCfg)
	group := "/conformance/" + name("events")
	createLogGroup(t, group, nil)
	for _, s := range []string{"app", "worker"} {
		_, err := client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(s)})
		ok(t, err, "CreateLogStream "+s)
	}
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	appMessages := []string{
		"INFO starting up",
		"ERROR database unavailable",
		`{"level":"error","latency":250,"user":{"id":7}}`,
		`{"level":"info","latency":20,"user":{"id":8}}`,
		"WARN slow request",
	}
	var events []types.InputLogEvent
	for i, m := range appMessages {
		events = append(events, types.InputLogEvent{Timestamp: aws.Int64(millis(base.Add(time.Duration(i) * time.Second))), Message: aws.String(m)})
	}
	put, err := client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), LogEvents: events})
	ok(t, err, "PutLogEvents")
	if aws.ToString(put.NextSequenceToken) == "" || put.RejectedLogEventsInfo != nil {
		t.Errorf("PutLogEvents = nextSequenceToken %q, rejected %+v; want a token and nothing rejected", aws.ToString(put.NextSequenceToken), put.RejectedLogEventsInfo)
	}
	_, err = client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("worker"), LogEvents: []types.InputLogEvent{
		{Timestamp: aws.Int64(millis(base.Add(2500 * time.Millisecond))), Message: aws.String("ERROR worker crashed")},
	}})
	ok(t, err, "PutLogEvents worker")

	t.Run("PutLogEvents errors", func(t *testing.T) {
		_, err := client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("nope"), LogEvents: events[:1]})
		errAs[*types.ResourceNotFoundException](t, err, "PutLogEvents to a missing stream")
		wantMessage(t, err, "PutLogEvents to a missing stream", "The specified log stream does not exist.")
		now := time.Now()
		_, err = client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), LogEvents: []types.InputLogEvent{
			{Timestamp: aws.Int64(millis(now)), Message: aws.String("later")},
			{Timestamp: aws.Int64(millis(now.Add(-time.Minute))), Message: aws.String("earlier")},
		}})
		errAs[*types.InvalidParameterException](t, err, "PutLogEvents out of order")
		wantMessage(t, err, "PutLogEvents out of order", "Log events in a single PutLogEvents request must be in chronological order.")
	})

	t.Run("rejected events", func(t *testing.T) {
		stream := "rejects"
		_, err := client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
		ok(t, err, "CreateLogStream")
		old, err := client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: []types.InputLogEvent{
			{Timestamp: aws.Int64(millis(time.Now().Add(-30 * 24 * time.Hour))), Message: aws.String("ancient")},
		}})
		ok(t, err, "PutLogEvents 30 days old")
		if old.RejectedLogEventsInfo == nil || old.RejectedLogEventsInfo.TooOldLogEventEndIndex == nil || aws.ToString(old.NextSequenceToken) == "" {
			t.Errorf("30-day-old event = rejected %+v, token %q; want tooOldLogEventEndIndex and a token", old.RejectedLogEventsInfo, aws.ToString(old.NextSequenceToken))
		}
		future, err := client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: []types.InputLogEvent{
			{Timestamp: aws.Int64(millis(time.Now().Add(24 * time.Hour))), Message: aws.String("tomorrow")},
		}})
		ok(t, err, "PutLogEvents a day ahead")
		if future.RejectedLogEventsInfo == nil || future.RejectedLogEventsInfo.TooNewLogEventStartIndex == nil || aws.ToInt32(future.RejectedLogEventsInfo.TooNewLogEventStartIndex) != 0 {
			t.Errorf("future event = rejected %+v; want tooNewLogEventStartIndex 0 (inclusive)", future.RejectedLogEventsInfo)
		}
		got, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true)})
		ok(t, err, "GetLogEvents")
		if len(got.Events) != 0 {
			t.Errorf("rejected events were stored: %+v", got.Events)
		}
	})

	t.Run("GetLogEvents", func(t *testing.T) {
		out, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), StartFromHead: aws.Bool(true)})
		ok(t, err, "GetLogEvents")
		var got []string
		for _, e := range out.Events {
			got = append(got, aws.ToString(e.Message))
			if e.IngestionTime == nil || e.Timestamp == nil {
				t.Errorf("event %q lacks timestamp or ingestionTime", aws.ToString(e.Message))
			}
		}
		if !slices.Equal(got, appMessages) {
			t.Errorf("GetLogEvents = %v, want %v", got, appMessages)
		}
		fwd, back := aws.ToString(out.NextForwardToken), aws.ToString(out.NextBackwardToken)
		if !strings.HasPrefix(fwd, "f/") || !strings.HasPrefix(back, "b/") {
			t.Errorf("tokens %q, %q; want f/ and b/ prefixes", fwd, back)
		}
		next, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), StartFromHead: aws.Bool(true), NextToken: aws.String(fwd)})
		ok(t, err, "GetLogEvents with the forward token")
		if len(next.Events) != 0 || aws.ToString(next.NextForwardToken) != fwd {
			t.Errorf("at the end of the stream = %d events, token %q; want none and the same token %q", len(next.Events), aws.ToString(next.NextForwardToken), fwd)
		}
		window, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName: aws.String(group), LogStreamName: aws.String("app"), StartFromHead: aws.Bool(true),
			StartTime: aws.Int64(millis(base.Add(time.Second))), EndTime: aws.Int64(millis(base.Add(3 * time.Second))),
		})
		ok(t, err, "GetLogEvents time window")
		if len(window.Events) != 2 || aws.ToString(window.Events[0].Message) != appMessages[1] {
			t.Errorf("window [1s, 3s) = %d events, want 2 (startTime inclusive, endTime exclusive)", len(window.Events))
		}
		paged, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), StartFromHead: aws.Bool(true), Limit: aws.Int32(2)})
		ok(t, err, "GetLogEvents limit 2")
		if len(paged.Events) != 2 || aws.ToString(paged.Events[0].Message) != appMessages[0] {
			t.Errorf("limit 2 from head = %d events", len(paged.Events))
		}
		_, err = client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), Limit: aws.Int32(10001)})
		errAs[*types.InvalidParameterException](t, err, "GetLogEvents limit 10001")
		wantMessage(t, err, "GetLogEvents limit 10001", "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000")
		_, err = client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("app"), NextToken: aws.String("f/garbage")})
		errAs[*types.InvalidParameterException](t, err, "GetLogEvents with an invalid token")
		wantMessage(t, err, "GetLogEvents with an invalid token", "The specified nextToken is invalid.")
	})

	t.Run("FilterLogEvents", func(t *testing.T) {
		filter := func(pattern string, streams ...string) []string {
			t.Helper()
			in := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), FilterPattern: aws.String(pattern), LogStreamNames: streams}
			var got []string
			p := cloudwatchlogs.NewFilterLogEventsPaginator(client, in)
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				ok(t, err, "FilterLogEvents "+pattern)
				for _, e := range page.Events {
					got = append(got, aws.ToString(e.Message))
				}
			}
			return got
		}
		cases := []struct {
			pattern string
			streams []string
			want    []string
		}{
			{"", nil, []string{appMessages[0], appMessages[1], appMessages[2], "ERROR worker crashed", appMessages[3], appMessages[4]}},
			{"ERROR", nil, []string{appMessages[1], "ERROR worker crashed"}},
			{"ERROR", []string{"app"}, []string{appMessages[1]}},
			{"ERROR database", nil, []string{appMessages[1]}},
			{"?WARN ?INFO", nil, []string{appMessages[0], appMessages[4]}},
			{`"slow request"`, nil, []string{appMessages[4]}},
			{"ERROR -worker", nil, []string{appMessages[1]}},
			{`{ $.level = "error" }`, nil, []string{appMessages[2]}},
			{`{ $.latency > 100 }`, nil, []string{appMessages[2]}},
			{`{ ($.user.id = 8) || ($.latency >= 250) }`, nil, []string{appMessages[2], appMessages[3]}},
			{`{ $.missing NOT EXISTS && $.level = "info" }`, nil, []string{appMessages[3]}},
			{"%unavail[a-z]+%", nil, []string{appMessages[1]}},
		}
		for _, c := range cases {
			if got := filter(c.pattern, c.streams...); !slices.Equal(got, c.want) {
				t.Errorf("FilterLogEvents %q (streams %v) = %v, want %v", c.pattern, c.streams, got, c.want)
			}
		}
		out, err := client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), FilterPattern: aws.String("ERROR")})
		ok(t, err, "FilterLogEvents")
		for _, e := range out.Events {
			if aws.ToString(e.EventId) == "" || aws.ToString(e.LogStreamName) == "" || e.IngestionTime == nil {
				t.Errorf("filtered event = %+v; want eventId, logStreamName and ingestionTime", e)
			}
		}
		if out.SearchedLogStreams == nil || len(out.SearchedLogStreams) != 0 {
			t.Errorf("searchedLogStreams = %#v, want an empty list", out.SearchedLogStreams)
		}
		page, err := client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), Limit: aws.Int32(2)})
		ok(t, err, "FilterLogEvents limit 2")
		if len(page.Events) != 2 || page.NextToken == nil {
			t.Errorf("FilterLogEvents limit 2 = %d events, nextToken %v; want 2 and a token", len(page.Events), page.NextToken)
		}
		_, err = client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), NextToken: aws.String("garbage")})
		errAs[*types.InvalidParameterException](t, err, "FilterLogEvents with an invalid token")
		wantMessage(t, err, "FilterLogEvents with an invalid token", "The specified nextToken is invalid.")
		_, err = client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), LogStreamNames: []string{"app"}, LogStreamNamePrefix: aws.String("a")})
		errAs[*types.InvalidParameterException](t, err, "FilterLogEvents with logStreamNames and a prefix")
		_, err = client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group + "-missing")})
		errAs[*types.ResourceNotFoundException](t, err, "FilterLogEvents on a missing group")
	})

	t.Run("DescribeLogStreams after events", func(t *testing.T) {
		out, err := client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(group), LogStreamNamePrefix: aws.String("app")})
		ok(t, err, "DescribeLogStreams")
		if len(out.LogStreams) != 1 {
			t.Fatalf("DescribeLogStreams = %+v", out.LogStreams)
		}
		s := out.LogStreams[0]
		if aws.ToInt64(s.FirstEventTimestamp) != millis(base) || aws.ToInt64(s.LastEventTimestamp) != millis(base.Add(4*time.Second)) || s.LastIngestionTime == nil {
			t.Errorf("stream = first %d, last %d, lastIngestion %v; want %d, %d", aws.ToInt64(s.FirstEventTimestamp), aws.ToInt64(s.LastEventTimestamp), s.LastIngestionTime, millis(base), millis(base.Add(4*time.Second)))
		}
	})
}
