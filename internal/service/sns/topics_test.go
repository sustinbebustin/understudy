package sns_test

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

const defaultPolicy = `{"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow","Principal":{"AWS":"*"},` +
	`"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission","SNS:DeleteTopic","SNS:Subscribe",` +
	`"SNS:ListSubscriptionsByTopic","SNS:Publish"],"Resource":"arn:aws:sns:us-east-1:000000000000:events","Condition":{"StringEquals":{"AWS:SourceOwner":"000000000000"}}}]}`

func TestNewTopicHasTheRecordedDefaultAttributes(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.topic("events", nil)
		if arn != "arn:aws:sns:us-east-1:000000000000:events" {
			h.Fatalf("TopicArn = %q", arn)
		}
		want := map[string]string{
			"TopicArn":                arn,
			"Owner":                   "000000000000",
			"DisplayName":             "",
			"Policy":                  defaultPolicy,
			"EffectiveDeliveryPolicy": `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"},"disableSubscriptionOverrides":false,"defaultRequestPolicy":{"headerContentType":"text/plain; charset=UTF-8"}}}`,
			"SubscriptionsConfirmed":  "0",
			"SubscriptionsDeleted":    "0",
			"SubscriptionsPending":    "0",
		}
		if got := h.topicAttributes(arn); !maps.Equal(got, want) {
			h.Errorf("GetTopicAttributes =\n%v\nwant\n%v", got, want)
		}
	})
}

func TestCreateTopicIsIdempotentAndRefusesConflicts(t *testing.T) {
	run(t, func(h *harness) {
		create := func(in *snsapi.CreateTopicInput) (*snsapi.CreateTopicOutput, error) {
			out, err := call(h, snsapi.OpCreateTopic, in)
			if err != nil {
				return nil, err.AsError()
			}
			return out, nil
		}
		first, err := create(&snsapi.CreateTopicInput{Name: "events", Attributes: map[string]string{"DisplayName": "Events"}, Tags: []snsapi.Tag{{Key: "team", Value: "a"}}})
		if err != nil {
			h.Fatal(err)
		}
		again, err := create(&snsapi.CreateTopicInput{Name: "events", Attributes: map[string]string{"DisplayName": "Events"}})
		if err != nil || *again.TopicArn != *first.TopicArn {
			h.Fatalf("CreateTopic again = %v, %v; want the same ARN", again, err)
		}
		// A standard topic accepts a repeated FifoTopic=false (sns.md 2.1).
		if _, err := create(&snsapi.CreateTopicInput{Name: "events", Attributes: map[string]string{"FifoTopic": "false"}}); err != nil {
			h.Fatal(err)
		}
		_, aerr := call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "events", Attributes: map[string]string{"DisplayName": "Other"}})
		wantError(h, aerr, "InvalidParameter", "Invalid parameter: Attributes Reason: Topic already exists with different attributes")
		_, aerr = call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "events", Tags: []snsapi.Tag{{Key: "team", Value: "a"}, {Key: "more", Value: "b"}}})
		wantError(h, aerr, "InvalidParameter", "Invalid parameter: Tags Reason: Topic already exists with different tags")
	})
}

func TestTopicNameRules(t *testing.T) {
	run(t, func(h *harness) {
		for _, tc := range []struct {
			name  string
			attrs map[string]string
		}{
			{name: ""},
			{name: strings.Repeat("a", 257)},
			{name: "bad name"},
			{name: "events.fifo"},
			{name: "events", attrs: map[string]string{"FifoTopic": "true"}},
		} {
			_, err := call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: tc.name, Attributes: tc.attrs})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Topic Name")
		}
		arn := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		attrs := h.topicAttributes(arn)
		if attrs["FifoTopic"] != "true" || attrs["ContentBasedDeduplication"] != "false" {
			h.Errorf("FIFO topic attributes = %v, want FifoTopic true and ContentBasedDeduplication false", attrs)
		}
		_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "FifoTopic", AttributeValue: new("false")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: AttributeName")
	})
}

func TestUnknownTopicAndOtherRegion(t *testing.T) {
	run(t, func(h *harness) {
		h.topic("events", nil)
		// Same-scope ARN, other-region ARN, other-account ARN (sns.md 1):
		// another region is refused; another account resolves there.
		_, err := call(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: "arn:aws:sns:eu-west-1:000000000000:events"})
		wantError(h, err, "InvalidParameter", "Invalid parameter: TopicArn")
		_, err = call(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: new("arn:aws:sns:eu-west-1:000000000000:events"), Message: "m"})
		wantError(h, err, "InvalidParameter", "Invalid parameter: TopicArn")
		_, err = call(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: "arn:aws:sns:us-east-1:111111111111:events"})
		wantError(h, err, "NotFound", "Topic does not exist")
		_, err = call(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: "arn:aws:sns:us-east-1:000000000000:missing"})
		wantError(h, err, "NotFound", "Topic does not exist")
		_, err = call(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: new("arn:aws:sns:us-east-1"), Message: "m"})
		wantError(h, err, "InvalidParameter", "Invalid parameter: TopicArn Reason: An ARN must have at least 6 elements, not 4")
	})
}

func TestDeleteTopicIsIdempotentAndListsPage(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 150 {
			h.topic("t"+strconv.Itoa(1000+i), nil)
		}
		var names []string
		var token *string
		for {
			out := must(h, snsapi.OpListTopics, &snsapi.ListTopicsInput{NextToken: token})
			for _, t := range out.Topics {
				names = append(names, *t.TopicArn)
			}
			if token = out.NextToken; token == nil {
				break
			}
		}
		if len(names) != 150 || names[0] != "arn:aws:sns:us-east-1:000000000000:t1000" || names[149] != "arn:aws:sns:us-east-1:000000000000:t1149" {
			h.Fatalf("ListTopics gave %d topics, first %q", len(names), names[0])
		}
		for range 2 {
			must(h, snsapi.OpDeleteTopic, &snsapi.DeleteTopicInput{TopicArn: names[0]})
		}
		_, err := call(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: names[0]})
		wantError(h, err, "NotFound", "Topic does not exist")
		_, err = call(h, snsapi.OpListTopics, &snsapi.ListTopicsInput{NextToken: new("not a token")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: NextToken")
	})
}

// TestTerraformTopicSequenceReadsBackWithoutDiff replays aws_sns_topic and
// aws_sns_topic_policy (tf-messaging.md 3.3, 3.4) and checks the reads
// Terraform maps.
func TestTerraformTopicSequenceReadsBackWithoutDiff(t *testing.T) {
	run(t, func(h *harness) {
		arn := *must(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "events", Tags: []snsapi.Tag{{Key: "env", Value: "test"}}}).TopicArn
		for _, p := range []string{"Application", "Firehose", "HTTP", "Lambda", "SQS"} {
			must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: p + "SuccessFeedbackSampleRate", AttributeValue: new("0")})
		}
		read := func() map[string]string {
			attrs := h.topicAttributes(arn)
			tags := must(h, snsapi.OpListTagsForResource, &snsapi.ListTagsForResourceInput{ResourceArn: arn}).Tags
			if len(tags) != 1 || tags[0].Key != "env" || tags[0].Value != "test" {
				h.Errorf("ListTagsForResource = %v", tags)
			}
			return attrs
		}
		first := read()
		for _, absent := range []string{"DeliveryPolicy", "KmsMasterKeyId", "ArchivePolicy", "SQSSuccessFeedbackRoleArn", "MaximumMessageSize", "FifoTopic"} {
			if v, ok := first[absent]; ok {
				h.Errorf("%s = %q on a bare topic; Terraform plans a diff for it", absent, v)
			}
		}
		if first["SQSSuccessFeedbackSampleRate"] != "0" || !json.Valid([]byte(first["Policy"])) {
			h.Errorf("read after create = %v", first)
		}
		if second := read(); !maps.Equal(first, second) {
			h.Errorf("second read differs:\n%v\n%v", first, second)
		}

		// aws_sns_topic_policy: the normalized policy reads back verbatim,
		// and delete writes Terraform's default with SNS:Receive back.
		policy := `{"Version":"2012-10-17","Statement":[{"Sid":"pub","Effect":"Allow","Principal":{"AWS":"*"},"Action":"SNS:Publish","Resource":"` + arn + `"}]}`
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "Policy", AttributeValue: &policy})
		if got := h.topicAttributes(arn)["Policy"]; got != policy {
			h.Errorf("Policy = %s, want %s", got, policy)
		}
		reset := strings.Replace(defaultPolicy, `"SNS:Publish"]`, `"SNS:Publish","SNS:Receive"]`, 1)
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "Policy", AttributeValue: &reset})
		if got := h.topicAttributes(arn)["Policy"]; got != reset {
			h.Errorf("Policy after reset = %s", got)
		}

		// Update: tags first, then one SetTopicAttributes per change.
		must(h, snsapi.OpTagResource, &snsapi.TagResourceInput{ResourceArn: arn, Tags: []snsapi.Tag{{Key: "env", Value: "test"}}})
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "DisplayName", AttributeValue: new("Events")})
		if got := read()["DisplayName"]; got != "Events" {
			h.Errorf("DisplayName = %q", got)
		}
		must(h, snsapi.OpDeleteTopic, &snsapi.DeleteTopicInput{TopicArn: arn})
	})
}

func TestFIFOTopicAttributes(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.topic("events.fifo", map[string]string{"FifoTopic": "true", "FifoThroughputScope": "MessageGroup"})
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "ContentBasedDeduplication", AttributeValue: new("true")})
		attrs := h.topicAttributes(arn)
		if attrs["ContentBasedDeduplication"] != "true" || attrs["FifoThroughputScope"] != "MessageGroup" {
			h.Errorf("FIFO attributes = %v", attrs)
		}
		// A MessageGroup scope cannot be reverted to Topic
		// (fifo-high-throughput.html); setting MessageGroup again is fine.
		setScope := func(arn, scope string) *awserr.Error {
			_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "FifoThroughputScope", AttributeValue: &scope})
			return err
		}
		wantError(h, setScope(arn, "Topic"), "InvalidParameter", "Invalid parameter: FifoThroughputScope Reason: a FifoThroughputScope of MessageGroup cannot be reverted to Topic")
		if err := setScope(arn, "MessageGroup"); err != nil {
			h.Errorf("SetTopicAttributes FifoThroughputScope=MessageGroup again = %s %q", err.ID.QueryCode, err.Message)
		}
		if got := h.topicAttributes(arn)["FifoThroughputScope"]; got != "MessageGroup" {
			h.Errorf("FifoThroughputScope after refused revert = %q, want MessageGroup", got)
		}
		// Topic to MessageGroup is the one-way change AWS allows.
		widening := h.topic("widening.fifo", map[string]string{"FifoTopic": "true", "FifoThroughputScope": "Topic"})
		if err := setScope(widening, "MessageGroup"); err != nil {
			h.Errorf("SetTopicAttributes FifoThroughputScope Topic to MessageGroup = %s %q", err.ID.QueryCode, err.Message)
		}
		std := h.topic("plain", nil)
		_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: std, AttributeName: "ContentBasedDeduplication", AttributeValue: new("true")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: ContentBasedDeduplication")
		_, err = call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: std, AttributeName: "Bogus", AttributeValue: new("x")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: AttributeName")
	})
}

// TestTopicDeliveryPolicyKeepsRetryConstraints: a topic DeliveryPolicy
// outside the documented retry and throttle constraints (sns.md 7.3) is
// refused at CreateTopic and SetTopicAttributes, with the numRetries text
// AWS-recorded in ballerina topics-tests.bal and the others in its form,
// and the stored policy is unchanged; the recorded valid policy and the
// bounds themselves are accepted.
func TestTopicDeliveryPolicyKeepsRetryConstraints(t *testing.T) {
	const valid = `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":10,"maxDelayTarget":20,"numRetries":3,"numNoDelayRetries":1,` +
		`"numMinDelayRetries":1,"numMaxDelayRetries":1,"backoffFunction":"linear"},"disableSubscriptionOverrides":true,` +
		`"defaultRequestPolicy":{"headerContentType":"application/json"}}}`
	retry := func(members string) string {
		return `{"http":{"defaultHealthyRetryPolicy":{` + members + `}}}`
	}
	run(t, func(h *harness) {
		arn := h.topic("events", map[string]string{"DeliveryPolicy": valid})
		setPolicy := func(v string) *awserr.Error {
			_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: &v})
			return err
		}
		for _, ok := range []string{
			retry(`"minDelayTarget":1,"maxDelayTarget":3600,"numRetries":100,"backoffFunction":"exponential"`),
			retry(`"numRetries":0`),
			retry(`"numRetries":3,"numNoDelayRetries":1,"numMinDelayRetries":1,"numMaxDelayRetries":1`),
			`{"http":{"defaultHealthyRetryPolicy":null,"defaultThrottlePolicy":{"maxReceivesPerSecond":1}}}`,
			valid,
		} {
			if err := setPolicy(ok); err != nil {
				h.Errorf("SetTopicAttributes DeliveryPolicy %s = %s %q", ok, err.ID.QueryCode, err.Message)
			}
		}
		for policy, detail := range map[string]string{
			retry(`"minDelayTarget":10,"maxDelayTarget":20,"numRetries":300,"numNoDelayRetries":1,"numMinDelayRetries":1,"numMaxDelayRetries":1,"backoffFunction":"linear"`): "http.defaultHealthyRetryPolicy.numRetries must be less than or equal to 100",
			retry(`"numRetries":-1`):                                             "http.defaultHealthyRetryPolicy.numRetries must be greater than or equal to 0",
			retry(`"minDelayTarget":0`):                                          "http.defaultHealthyRetryPolicy.minDelayTarget must be greater than or equal to 1",
			retry(`"minDelayTarget":30,"maxDelayTarget":20`):                     "http.defaultHealthyRetryPolicy.minDelayTarget must be less than or equal to 20",
			retry(`"maxDelayTarget":3601`):                                       "http.defaultHealthyRetryPolicy.maxDelayTarget must be less than or equal to 3600",
			retry(`"numNoDelayRetries":-2`):                                      "http.defaultHealthyRetryPolicy.numNoDelayRetries must be greater than or equal to 0",
			retry(`"numRetries":2,"numNoDelayRetries":2,"numMaxDelayRetries":1`): "http.defaultHealthyRetryPolicy.numRetries must be greater than or equal to 3",
			retry(`"backoffFunction":"cubic"`):                                   "http.defaultHealthyRetryPolicy.backoffFunction must be one of arithmetic, exponential, geometric, linear",
			`{"http":{"defaultThrottlePolicy":{"maxReceivesPerSecond":0}}}`:      "http.defaultThrottlePolicy.maxReceivesPerSecond must be greater than or equal to 1",
		} {
			wantError(h, setPolicy(policy), "InvalidParameter", "Invalid parameter: DeliveryPolicy: "+detail)
			_, err := call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "refused", Attributes: map[string]string{"DeliveryPolicy": policy}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: DeliveryPolicy: "+detail)
		}
		if got := h.topicAttributes(arn)["DeliveryPolicy"]; !strings.Contains(got, `"disableSubscriptionOverrides":true`) {
			h.Errorf("DeliveryPolicy after the refused values = %s, want the recorded valid policy", got)
		}
	})
}

// TestTopicPolicyActionsStayInServiceScope: a topic Policy naming an
// action SNS does not serve is refused at CreateTopic (the policy and
// text AWS-recorded in ballerina topics-tests.bal) and SetTopicAttributes;
// SNS actions in any case, sns:* patterns that match one, and "*" are
// accepted.
func TestTopicPolicyActionsStayInServiceScope(t *testing.T) {
	statement := func(action string) string {
		return `{"Version":"2008-10-17","Id":"p","Statement":[{"Sid":"s","Effect":"Allow","Principal":{"AWS":"*"},"Action":` + action +
			`,"Resource":"arn:aws:sns:us-east-1:000000000000:events"}]}`
	}
	run(t, func(h *harness) {
		const recorded = `{"Version": "2008-10-17", "Id": "__default_policy_ID", "Statement": [{"Sid": "__default_statement_ID", "Effect": "Allow", ` +
			`"Principal": {"AWS": "*"}, "Action": ["SNS:Publishx", "SNS:RemovePermission", "SNS:SetTopicAttributes", "SNS:DeleteTopic", ` +
			`"SNS:ListSubscriptionsByTopic", "SNS:GetTopicAttributes", "SNS:AddPermission", "SNS:Subscribe"], "Resource": "", ` +
			`"Condition": {"StringEquals": {"AWS:SourceOwner": "482724125666"}}}, {"Sid": "__console_sub_0", "Effect": "Allow", ` +
			`"Principal": {"AWS": "*"}, "Action": ["SNS:Subscribe"], "Resource": ""}]}`
		_, err := call(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "refused", Attributes: map[string]string{"Policy": recorded}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Policy statement action out of service scope!")

		arn := h.topic("events", nil)
		setPolicy := func(v string) *awserr.Error {
			_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "Policy", AttributeValue: &v})
			return err
		}
		for _, action := range []string{`"sqs:SendMessage"`, `["SNS:Publish","SNS:CreateTopic"]`, `"sns:Pub*x"`, `"Publish"`} {
			wantError(h, setPolicy(statement(action)), "InvalidParameter", "Invalid parameter: Policy statement action out of service scope!")
		}
		for _, action := range []string{`"sns:publish"`, `"SNS:Receive"`, `"sns:ListTagsForResource"`, `"sns:*"`, `"SNS:Get*"`, `"*"`} {
			if err := setPolicy(statement(action)); err != nil {
				h.Errorf("SetTopicAttributes Policy with Action %s = %s %q", action, err.ID.QueryCode, err.Message)
			}
		}
	})
}

func TestDeliveryPolicyRoundTripsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.topic("events", nil)
		set := func(v string) {
			must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: &v})
		}
		set(`{"http":{"defaultRequestPolicy":{"headerContentType":"application/json"}}}`)
		attrs := h.topicAttributes(arn)
		if want := `{"http":{"defaultRequestPolicy":{"headerContentType":"application/json"},"disableSubscriptionOverrides":false}}`; attrs["DeliveryPolicy"] != want {
			h.Errorf("DeliveryPolicy = %s, want %s", attrs["DeliveryPolicy"], want)
		}
		if !strings.Contains(attrs["EffectiveDeliveryPolicy"], `"headerContentType":"application/json"`) {
			h.Errorf("EffectiveDeliveryPolicy = %s", attrs["EffectiveDeliveryPolicy"])
		}
		set("")
		if _, ok := h.topicAttributes(arn)["DeliveryPolicy"]; ok {
			h.Error("DeliveryPolicy set to the empty string is still shown")
		}
	})
}

func TestTagsAndPermissions(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.topic("events", nil)
		_, err := call(h, snsapi.OpTagResource, &snsapi.TagResourceInput{ResourceArn: arn, Tags: []snsapi.Tag{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Duplicated keys are not allowed.")
		must(h, snsapi.OpTagResource, &snsapi.TagResourceInput{ResourceArn: arn, Tags: []snsapi.Tag{{Key: "b", Value: "1"}, {Key: "a", Value: "1"}}})
		must(h, snsapi.OpTagResource, &snsapi.TagResourceInput{ResourceArn: arn, Tags: []snsapi.Tag{{Key: "a", Value: "2"}}})
		must(h, snsapi.OpUntagResource, &snsapi.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"b"}})
		tags := must(h, snsapi.OpListTagsForResource, &snsapi.ListTagsForResourceInput{ResourceArn: arn}).Tags
		if len(tags) != 1 || tags[0] != (snsapi.Tag{Key: "a", Value: "2"}) {
			h.Errorf("tags = %v, want a=2", tags)
		}
		_, err = call(h, snsapi.OpListTagsForResource, &snsapi.ListTagsForResourceInput{ResourceArn: "arn:aws:sns:us-east-1:000000000000:missing"})
		wantError(h, err, "ResourceNotFound", "Resource does not exist")

		must(h, snsapi.OpAddPermission, &snsapi.AddPermissionInput{TopicArn: arn, Label: "pub", AWSAccountId: []string{"111111111111"}, ActionName: []string{"Publish"}})
		policy := h.topicAttributes(arn)["Policy"]
		want := `{"Sid":"pub","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:root"},"Action":"SNS:Publish","Resource":"` + arn + `"}`
		if !strings.Contains(policy, want) || !strings.Contains(policy, "__default_statement_ID") {
			h.Errorf("Policy after AddPermission = %s", policy)
		}
		_, err = call(h, snsapi.OpAddPermission, &snsapi.AddPermissionInput{TopicArn: arn, Label: "pub", AWSAccountId: []string{"111111111111"}, ActionName: []string{"Publish"}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Statement already exists")
		_, err = call(h, snsapi.OpAddPermission, &snsapi.AddPermissionInput{TopicArn: arn, Label: "x", AWSAccountId: []string{"111111111111"}, ActionName: []string{"CreateTopic"}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Policy statement action out of service scope!")
		// AWS-recorded in ballerina permission-tests.bal.
		_, err = call(h, snsapi.OpAddPermission, &snsapi.AddPermissionInput{TopicArn: arn, Label: "bad", AWSAccountId: []string{"111111111111", "InvalidAccountId"}, ActionName: []string{"Publish"}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: A provided account ID is not valid")
		if policy := h.topicAttributes(arn)["Policy"]; strings.Contains(policy, "InvalidAccountId") || strings.Contains(policy, `"bad"`) {
			h.Errorf("Policy after a refused AddPermission = %s", policy)
		}
		must(h, snsapi.OpRemovePermission, &snsapi.RemovePermissionInput{TopicArn: arn, Label: "pub"})
		if policy := h.topicAttributes(arn)["Policy"]; strings.Contains(policy, `"pub"`) {
			h.Errorf("Policy after RemovePermission = %s", policy)
		}
	})
}
