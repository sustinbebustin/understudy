package logs_test

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The aws_cloudwatch_log_group sequence of tf-compute.md 5: create with
// tags, set retention, read by prefix and filter to the exact name, list
// tags on the ARN without ":*", clear retention, delete. Each read must
// show what the configuration declared, so a second plan has no diff.
func TestTerraformLogGroupSequenceReadsBackItsConfiguration(t *testing.T) {
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{
			LogGroupName: "/aws/lambda/worker",
			Tags:         map[string]string{"env": "test"},
		})
		h.group("/aws/lambda/worker-2") // a sibling the prefix also selects
		must(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "/aws/lambda/worker", RetentionInDays: 14})

		read := func() logsapi.LogGroup {
			h.Helper()
			out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("/aws/lambda/worker")})
			if len(out.LogGroups) != 2 {
				h.Fatalf("DescribeLogGroups by prefix = %d groups, want 2", len(out.LogGroups))
			}
			for _, g := range out.LogGroups {
				if val(g.LogGroupName) == "/aws/lambda/worker" {
					return g
				}
			}
			h.Fatal("the exact name is not in the listing")
			return logsapi.LogGroup{}
		}
		g := read()
		const arn = "arn:aws:logs:us-east-1:000000000000:log-group:/aws/lambda/worker"
		switch {
		case val(g.Arn) != arn+":*":
			h.Errorf("arn = %s, want the form with :*", val(g.Arn))
		case val(g.LogGroupArn) != arn:
			h.Errorf("logGroupArn = %s", val(g.LogGroupArn))
		case g.RetentionInDays == nil || *g.RetentionInDays != 14:
			h.Errorf("retentionInDays = %v, want 14", g.RetentionInDays)
		case g.LogGroupClass == nil || *g.LogGroupClass != logsapi.LogGroupClassStandard:
			h.Errorf("logGroupClass = %v, want STANDARD", g.LogGroupClass)
		case g.DeletionProtectionEnabled == nil || *g.DeletionProtectionEnabled:
			h.Errorf("deletionProtectionEnabled = %v, want false", g.DeletionProtectionEnabled)
		case g.KmsKeyId != nil:
			h.Errorf("kmsKeyId = %s, want absent", *g.KmsKeyId)
		case g.CreationTime == nil || *g.CreationTime != h.now():
			h.Errorf("creationTime = %v, want %d", g.CreationTime, h.now())
		}
		tags := must(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: arn})
		if !maps.Equal(tags.Tags, map[string]string{"env": "test"}) {
			h.Errorf("ListTagsForResource = %v", tags.Tags)
		}

		must(h, logsapi.OpDeleteRetentionPolicy, &logsapi.DeleteRetentionPolicyInput{LogGroupName: "/aws/lambda/worker"})
		if g := read(); g.RetentionInDays != nil {
			h.Errorf("retentionInDays after DeleteRetentionPolicy = %d, want absent", *g.RetentionInDays)
		}
		must(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "/aws/lambda/worker"})
		_, err := call(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "/aws/lambda/worker"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

// deletion_protection_enabled of tf-compute.md 5: a group created
// protected refuses DeleteLogGroup until PutLogGroupDeletionProtection,
// called with the group's ARN as Terraform does, turns it off; then
// destroy succeeds.
func TestTerraformDeletionProtectionTogglesOff(t *testing.T) {
	run(t, func(h *harness) {
		const arn = "arn:aws:logs:us-east-1:000000000000:log-group:protected"
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "protected", DeletionProtectionEnabled: new(true)})
		protection := func() bool {
			h.Helper()
			out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("protected")})
			if len(out.LogGroups) != 1 || out.LogGroups[0].DeletionProtectionEnabled == nil {
				h.Fatalf("DescribeLogGroups = %+v", out.LogGroups)
			}
			return *out.LogGroups[0].DeletionProtectionEnabled
		}
		if !protection() {
			h.Error("deletionProtectionEnabled after create = false, want true")
		}
		if _, err := call(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "protected"}); err == nil {
			h.Fatal("DeleteLogGroup of a protected group succeeded")
		}

		must(h, logsapi.OpPutLogGroupDeletionProtection, &logsapi.PutLogGroupDeletionProtectionInput{LogGroupIdentifier: arn, DeletionProtectionEnabled: false})
		if protection() {
			h.Error("deletionProtectionEnabled after turning it off = true, want false")
		}
		must(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "protected"})

		_, err := call(h, logsapi.OpPutLogGroupDeletionProtection, &logsapi.PutLogGroupDeletionProtectionInput{LogGroupIdentifier: "protected", DeletionProtectionEnabled: true})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

// The aws_cloudwatch_log_stream sequence of tf-remaining.md 7: create, read
// by prefix filtered to the exact name, delete; a stream without events
// shows only arn, creationTime, logStreamName and storedBytes.
func TestTerraformLogStreamSequence(t *testing.T) {
	run(t, func(h *harness) {
		h.group("/g6/group")
		h.stream("/g6/group", "stream-1")
		h.stream("/g6/group", "stream-10")
		out := must(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{
			LogGroupName: new("/g6/group"), LogStreamNamePrefix: new("stream-1"),
		})
		if len(out.LogStreams) != 2 {
			h.Fatalf("DescribeLogStreams by prefix = %d streams, want 2", len(out.LogStreams))
		}
		s := out.LogStreams[0]
		want := logsapi.LogStream{
			LogStreamName: new("stream-1"),
			CreationTime:  new(h.now()),
			Arn:           new("arn:aws:logs:us-east-1:000000000000:log-group:/g6/group:log-stream:stream-1"),
			StoredBytes:   new(int64(0)),
		}
		if val(s.LogStreamName) != *want.LogStreamName || val(s.Arn) != *want.Arn || *s.CreationTime != *want.CreationTime ||
			*s.StoredBytes != 0 || s.FirstEventTimestamp != nil || s.LastEventTimestamp != nil || s.LastIngestionTime != nil || s.UploadSequenceToken != nil {
			h.Errorf("empty stream = %+v", s)
		}
		_, err := call(h, logsapi.OpCreateLogStream, &logsapi.CreateLogStreamInput{LogGroupName: "/g6/group", LogStreamName: "stream-1"})
		wantError(h.T, err, "ResourceAlreadyExistsException", "The specified log stream already exists")

		must(h, logsapi.OpDeleteLogStream, &logsapi.DeleteLogStreamInput{LogGroupName: "/g6/group", LogStreamName: "stream-1"})
		_, err = call(h, logsapi.OpDeleteLogStream, &logsapi.DeleteLogStreamInput{LogGroupName: "/g6/group", LogStreamName: "stream-1"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log stream does not exist.")
		_, err = call(h, logsapi.OpCreateLogStream, &logsapi.CreateLogStreamInput{LogGroupName: "/missing", LogStreamName: "s"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

func TestStreamListingOrderAndPaging(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		for _, name := range []string{"c", "a", "b"} {
			h.stream("g", name)
		}
		// b gets the newest event, a the oldest; c has none.
		now := h.now()
		h.put("g", "a", event(now-2000, "x"))
		h.put("g", "b", event(now-1000, "x"))

		list := func(in *logsapi.DescribeLogStreamsInput) []string {
			h.Helper()
			var names []string
			for {
				out := must(h, logsapi.OpDescribeLogStreams, in)
				for _, s := range out.LogStreams {
					names = append(names, val(s.LogStreamName))
				}
				if out.NextToken == nil {
					return names
				}
				in.NextToken = out.NextToken
			}
		}
		if got := list(&logsapi.DescribeLogStreamsInput{LogGroupName: new("g"), Limit: new(int32(1))}); strings.Join(got, ",") != "a,b,c" {
			h.Errorf("by name, one per page = %v", got)
		}
		if got := list(&logsapi.DescribeLogStreamsInput{LogGroupName: new("g"), Descending: new(true), Limit: new(int32(2))}); strings.Join(got, ",") != "c,b,a" {
			h.Errorf("by name descending = %v", got)
		}
		byTime := &logsapi.DescribeLogStreamsInput{LogGroupIdentifier: new("arn:aws:logs:us-east-1:000000000000:log-group:g"), OrderBy: new(logsapi.OrderByLastEventTime), Descending: new(true)}
		if got := list(byTime); strings.Join(got, ",") != "b,a,c" {
			h.Errorf("by last event time descending = %v", got)
		}

		_, err := call(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{LogGroupName: new("g"), LogGroupIdentifier: new("g")})
		wantError(h.T, err, "ValidationException", "LogGroup name and LogGroup ARN are mutually exclusive parameters.")
		_, err = call(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{LogGroupName: new("g"), NextToken: new("not a token")})
		wantError(h.T, err, "InvalidParameterException", "The specified nextToken is invalid.")
		_, err = call(h, logsapi.OpDescribeLogStreams, &logsapi.DescribeLogStreamsInput{LogGroupIdentifier: new("arn:aws:logs:eu-west-1:000000000000:log-group:g")})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

func TestGroupErrors(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		_, err := call(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "g"})
		wantError(h.T, err, "ResourceAlreadyExistsException", "The specified log group already exists")
		_, err = call(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("g"), LogGroupNamePattern: new("g")})
		wantError(h.T, err, "InvalidParameterException", "LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters.")
		_, err = call(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "g", RetentionInDays: 2})
		// Recorded: testdata/parity/logs/R10/06-retention-6.json.
		wantError(h.T, err, "InvalidParameterException",
			"Invalid retention value. Valid values are: [1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653]")
		_, err = call(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "missing", RetentionInDays: 1})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")

		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "protected", DeletionProtectionEnabled: new(true)})
		_, err = call(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "protected"})
		if err == nil || err.ID.JSONType != "ValidationException" {
			h.Errorf("DeleteLogGroup of a protected group = %+v, want ValidationException", err)
		}

		// "aws/" is reserved; "/aws/..." is the Lambda form and stays valid.
		_, err = call(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "aws/reserved"})
		wantError(h.T, err, "InvalidParameterException", "Log group names can't start with the string aws/.")
		h.group("/aws/lambda/fn")
	})
}

// A log group ARN of another partition names no group, though its
// account, region and name match the caller's group.
func TestGroupARNOfAnotherPartitionNamesNoGroup(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		_, err := call(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: "arn:aws-cn:logs:us-east-1:000000000000:log-group:g"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

// Both tag families operate on one tag set (secrets-logs-events-alarms.md
// 3.7).
func TestTagFamiliesShareOneTagSet(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		const arn = "arn:aws:logs:us-east-1:000000000000:log-group:g"
		must(h, logsapi.OpTagLogGroup, &logsapi.TagLogGroupInput{LogGroupName: "g", Tags: map[string]string{"a": "1"}})
		must(h, logsapi.OpTagResource, &logsapi.TagResourceInput{ResourceArn: arn, Tags: map[string]string{"b": "2"}})
		want := map[string]string{"a": "1", "b": "2"}
		if got := must(h, logsapi.OpListTagsLogGroup, &logsapi.ListTagsLogGroupInput{LogGroupName: "g"}).Tags; !maps.Equal(got, want) {
			h.Errorf("ListTagsLogGroup = %v, want %v", got, want)
		}
		must(h, logsapi.OpUntagResource, &logsapi.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"a"}})
		must(h, logsapi.OpUntagLogGroup, &logsapi.UntagLogGroupInput{LogGroupName: "g", Tags: []string{"b"}})
		got := must(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: arn}).Tags
		if got == nil || len(got) != 0 {
			h.Errorf("ListTagsForResource after untagging = %v, want an empty map", got)
		}
		_, err := call(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: arn + "-missing"})
		wantError(h.T, err, "ResourceNotFoundException", "The specified log group does not exist.")
	})
}

func TestDescribeLogGroupsPages(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"b", "d", "a", "c", "e"} {
			h.group(name)
		}
		in := &logsapi.DescribeLogGroupsInput{Limit: new(int32(2))}
		var got []string
		for {
			out := must(h, logsapi.OpDescribeLogGroups, in)
			if len(out.LogGroups) > 2 {
				h.Fatalf("a page of %d groups with limit 2", len(out.LogGroups))
			}
			for _, g := range out.LogGroups {
				got = append(got, val(g.LogGroupName))
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if strings.Join(got, ",") != "a,b,c,d,e" {
			h.Errorf("pages = %v", got)
		}
		out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePattern: new("c")})
		if len(out.LogGroups) != 1 || val(out.LogGroups[0].LogGroupName) != "c" {
			h.Errorf("by pattern = %+v", out.LogGroups)
		}
	})
}

// DescribeLogGroups with logGroupNamePattern returns the field set AWS
// records for it (test_describe_log_groups_with_pattern): arn,
// creationTime, logGroupArn, logGroupClass, logGroupName and
// metricFilterCount. storedBytes, deletionProtectionEnabled and
// retentionInDays stay out even when set; a prefix listing keeps them.
func TestDescribeLogGroupsByPatternReturnsTheRecordedFields(t *testing.T) {
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "/app/a", DeletionProtectionEnabled: new(true)})
		must(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "/app/a", RetentionInDays: 14})
		const arn = "arn:aws:logs:us-east-1:000000000000:log-group:/app/a"

		out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePattern: new("app")})
		want := logsapi.LogGroup{
			Arn:               new(arn + ":*"),
			CreationTime:      new(h.now()),
			LogGroupArn:       new(arn),
			LogGroupClass:     new(logsapi.LogGroupClassStandard),
			LogGroupName:      new("/app/a"),
			MetricFilterCount: new(int32(0)),
		}
		if len(out.LogGroups) != 1 || !reflect.DeepEqual(out.LogGroups[0], want) {
			h.Errorf("by pattern = %+v\nwant [%+v]", out.LogGroups, want)
		}

		out = must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("/app")})
		if len(out.LogGroups) != 1 {
			h.Fatalf("by prefix = %d groups, want 1", len(out.LogGroups))
		}
		g := out.LogGroups[0]
		if g.StoredBytes == nil || g.DeletionProtectionEnabled == nil || !*g.DeletionProtectionEnabled || g.RetentionInDays == nil || *g.RetentionInDays != 14 {
			h.Errorf("by prefix = storedBytes %v, deletionProtectionEnabled %v, retentionInDays %v; want all present", g.StoredBytes, g.DeletionProtectionEnabled, g.RetentionInDays)
		}
	})
}

// A Request fault rule scoped to a group's ARN throttles the tagging calls
// that name it before their handlers run, and leaves other groups alone.
func TestFaultRulesMatchTheGroupATaggingCallNames(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.group("other")
		const (
			group = "arn:aws:logs:us-east-1:000000000000:log-group:g"
			other = "arn:aws:logs:us-east-1:000000000000:log-group:other"
		)
		if _, err := h.Faults.Add(fault.Rule{
			Match:   fault.Match{Services: []ref.ServiceID{"logs"}, Resource: fault.NewARNGlob(group)},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.Always{},
		}); err != nil {
			h.Fatal(err)
		}
		for _, c := range []struct {
			arn  string
			want bool
		}{{group, true}, {other, false}} {
			_, tag := call(h, logsapi.OpTagResource, &logsapi.TagResourceInput{ResourceArn: c.arn, Tags: map[string]string{"k": "v"}})
			_, untag := call(h, logsapi.OpUntagResource, &logsapi.UntagResourceInput{ResourceArn: c.arn, TagKeys: []string{"k"}})
			_, list := call(h, logsapi.OpListTagsForResource, &logsapi.ListTagsForResourceInput{ResourceArn: c.arn})
			for op, err := range map[string]*awserr.Error{"TagResource": tag, "UntagResource": untag, "ListTagsForResource": list} {
				if got := injected(err); got != c.want {
					h.Errorf("%s on %s injected = %v (error %v), want %v", op, c.arn, got, err, c.want)
				}
			}
		}
	})
}

// Deletion protection blocks every deletion in the group, its streams'
// included, until it is turned off (CloudWatchLogsConcepts.html); the
// group's refusal carries the user guide's text.
func TestDeletionProtectionCoversStreams(t *testing.T) {
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "protected", DeletionProtectionEnabled: new(true)})
		h.stream("protected", "s")
		h.put("protected", "s", event(h.now(), "kept"))

		_, err := call(h, logsapi.OpDeleteLogStream, &logsapi.DeleteLogStreamInput{LogGroupName: "protected", LogStreamName: "s"})
		wantError(h.T, err, "ValidationException", "Cannot delete log stream in a log group with deletion protection enabled. Disable deletion protection first.")
		_, err = call(h, logsapi.OpDeleteLogGroup, &logsapi.DeleteLogGroupInput{LogGroupName: "protected"})
		wantError(h.T, err, "ValidationException", "Cannot delete log group with deletion protection enabled. Disable deletion protection first.")
		if got := h.all("protected", "s"); len(got) != 1 {
			h.Errorf("events after refused deletes = %v, want the stored one", got)
		}

		must(h, logsapi.OpPutLogGroupDeletionProtection, &logsapi.PutLogGroupDeletionProtectionInput{LogGroupIdentifier: "protected", DeletionProtectionEnabled: false})
		must(h, logsapi.OpDeleteLogStream, &logsapi.DeleteLogStreamInput{LogGroupName: "protected", LogStreamName: "s"})
	})
}

// logGroupIdentifiers takes no filter but includeLinkedAccounts
// (DescribeLogGroups documentation), so a request adding a prefix, pattern
// or class is refused instead of answering an empty listing.
func TestDescribeLogGroupsIdentifiersExcludeOtherFilters(t *testing.T) {
	run(t, func(h *harness) {
		h.group("/app/a")
		const want = "logGroupIdentifiers cannot be combined with logGroupNamePrefix, logGroupNamePattern or logGroupClass."
		for name, in := range map[string]*logsapi.DescribeLogGroupsInput{
			"prefix":  {LogGroupIdentifiers: []string{"/app/a"}, LogGroupNamePrefix: new("/other")},
			"pattern": {LogGroupIdentifiers: []string{"/app/a"}, LogGroupNamePattern: new("app")},
			"class":   {LogGroupIdentifiers: []string{"/app/a"}, LogGroupClass: new(logsapi.LogGroupClassStandard)},
		} {
			_, err := call(h, logsapi.OpDescribeLogGroups, in)
			if err == nil || err.ID.JSONType != "InvalidParameterException" || err.Message != want {
				h.Errorf("identifiers with %s = %+v, want InvalidParameterException %q", name, err, want)
			}
		}
		out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{
			LogGroupIdentifiers: []string{"arn:aws:logs:us-east-1:000000000000:log-group:/app/a"}, IncludeLinkedAccounts: new(true),
		})
		if len(out.LogGroups) != 1 || val(out.LogGroups[0].LogGroupName) != "/app/a" {
			h.Errorf("identifiers with includeLinkedAccounts = %+v", out.LogGroups)
		}
	})
}

// A DELIVERY group keeps its events two days, a period the log class
// documentation fixes: DescribeLogGroups reports it, the retention
// policy operations cannot change it, and events expire at it on the
// emulator clock.
func TestDeliveryClassRetentionIsFixed(t *testing.T) {
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "d", LogGroupClass: new(logsapi.LogGroupClassDelivery)})
		out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("d")})
		if len(out.LogGroups) != 1 || out.LogGroups[0].RetentionInDays == nil || *out.LogGroups[0].RetentionInDays != 2 {
			h.Fatalf("DescribeLogGroups = %+v, want retentionInDays 2", out.LogGroups)
		}

		const fixed = "The retention period of a log group in the DELIVERY log class is fixed and cannot be changed."
		_, err := call(h, logsapi.OpPutRetentionPolicy, &logsapi.PutRetentionPolicyInput{LogGroupName: "d", RetentionInDays: 365})
		wantError(h.T, err, "InvalidParameterException", fixed)
		_, err = call(h, logsapi.OpDeleteRetentionPolicy, &logsapi.DeleteRetentionPolicyInput{LogGroupName: "d"})
		wantError(h.T, err, "InvalidParameterException", fixed)

		h.stream("d", "s")
		h.put("d", "s", event(h.now(), "short-lived"))
		h.advance(2*day - time.Millisecond)
		if got := h.all("d", "s"); len(got) != 1 {
			h.Errorf("just before two days = %v, want the event", got)
		}
		h.advance(time.Millisecond)
		if got := h.all("d", "s"); len(got) != 0 {
			h.Errorf("after two days = %v, want none", got)
		}
	})
}

// TagLogGroup's model lists no TooManyTagsException, so its over-limit
// error is InvalidParameterException; TagResource keeps
// TooManyTagsException. Neither changes the tag set.
func TestTagLimitErrorFollowsEachFamilysModel(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		const arn = "arn:aws:logs:us-east-1:000000000000:log-group:g"
		tags := map[string]string{}
		for i := range 50 {
			tags["k"+strconv.Itoa(i)] = "v"
		}
		must(h, logsapi.OpTagLogGroup, &logsapi.TagLogGroupInput{LogGroupName: "g", Tags: tags})
		const want = "A resource can have no more than 50 tags: " + arn

		_, err := call(h, logsapi.OpTagLogGroup, &logsapi.TagLogGroupInput{LogGroupName: "g", Tags: map[string]string{"extra": "v"}})
		wantError(h.T, err, "InvalidParameterException", want)
		_, err = call(h, logsapi.OpTagResource, &logsapi.TagResourceInput{ResourceArn: arn, Tags: map[string]string{"extra": "v"}})
		wantError(h.T, err, "TooManyTagsException", want)
		// The model gives TooManyTagsException a resourceName member.
		if !slices.Contains(err.Fields, awserr.Field{Name: "resourceName", Value: arn}) {
			h.Errorf("TooManyTagsException fields = %v, want resourceName %s", err.Fields, arn)
		}
		if got := must(h, logsapi.OpListTagsLogGroup, &logsapi.ListTagsLogGroupInput{LogGroupName: "g"}).Tags; !maps.Equal(got, tags) {
			h.Errorf("tags after refused additions = %d keys, want the 50 set", len(got))
		}
	})
}

func injected(err *awserr.Error) bool {
	if err == nil {
		return false
	}
	_, ok := err.Origin.(awserr.Injected)
	return ok
}
