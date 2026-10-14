package sqs_test

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// runWith is run with the service built from opts.
func runWith(t *testing.T, opts sqs.Options, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := sqs.New(k.Deps(sqsapi.Service()), opts)
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(sqsapi.Service()))
}

func (h *harness) attributes(url string, names ...sqsapi.QueueAttributeName) map[sqsapi.QueueAttributeName]string {
	h.Helper()
	out, err := h.Invoke(h.Context(), sqsapi.OpGetQueueAttributes, &sqsapi.GetQueueAttributesInput{QueueUrl: url, AttributeNames: names})
	if err != nil {
		h.Fatalf("GetQueueAttributes = %s: %s", err.ID.QueryCode, err.Message)
	}
	return out.Attributes
}

func (h *harness) setAttributes(url string, attrs map[sqsapi.QueueAttributeName]string) *awserr.Error {
	_, err := h.Invoke(h.Context(), sqsapi.OpSetQueueAttributes, &sqsapi.SetQueueAttributesInput{QueueUrl: url, Attributes: attrs})
	return err
}

func (h *harness) deleteQueue(url string) *awserr.Error {
	_, err := h.Invoke(h.Context(), sqsapi.OpDeleteQueue, &sqsapi.DeleteQueueInput{QueueUrl: url})
	return err
}

// sqs.md 5.2: a case-sensitive name prefix, pages sorted by name, and a
// NextToken only when MaxResults was set and more follow.
func TestListQueuesPages(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []string{"a3", "a1", "b1", "a2", "A0"} {
			h.queue(n, nil)
		}
		list := func(in *sqsapi.ListQueuesInput) *sqsapi.ListQueuesOutput {
			h.Helper()
			out, err := h.Invoke(h.Context(), sqsapi.OpListQueues, in)
			if err != nil {
				h.Fatalf("ListQueues = %s: %s", err.ID.QueryCode, err.Message)
			}
			return out
		}
		url := func(n string) string { return "http://localhost:4566/000000000000/" + n }
		if out := list(&sqsapi.ListQueuesInput{QueueNamePrefix: new("a")}); !slices.Equal(out.QueueUrls, []string{url("a1"), url("a2"), url("a3")}) || out.NextToken != nil {
			h.Errorf("prefix a = %+v", out)
		}
		first := list(&sqsapi.ListQueuesInput{QueueNamePrefix: new("a"), MaxResults: new(int32(2))})
		if !slices.Equal(first.QueueUrls, []string{url("a1"), url("a2")}) || first.NextToken == nil {
			h.Fatalf("first page = %+v", first)
		}
		second := list(&sqsapi.ListQueuesInput{QueueNamePrefix: new("a"), MaxResults: new(int32(2)), NextToken: first.NextToken})
		if !slices.Equal(second.QueueUrls, []string{url("a3")}) || second.NextToken != nil {
			h.Errorf("second page = %+v", second)
		}
		if out := list(&sqsapi.ListQueuesInput{QueueNamePrefix: new("z")}); out.QueueUrls != nil {
			h.Errorf("no match = %+v, want no QueueUrls", out)
		}
		_, err := h.Invoke(h.Context(), sqsapi.OpListQueues, &sqsapi.ListQueuesInput{MaxResults: new(int32(0))})
		wantError(h.T, err, "InvalidParameterValue", "Value 0 for parameter MaxResults is invalid. Reason: Must be between 1 and 1000, if provided.")
		_, err = h.Invoke(h.Context(), sqsapi.OpListQueues, &sqsapi.ListQueuesInput{NextToken: new("!")})
		wantError(h.T, err, "InvalidParameterValue", "Invalid NextToken value.")
	})
}

// The default reuses a deleted name at once (ARCHITECTURE.md 9); the
// enforce switch answers QueueDeletedRecently for 60 s of emulator time.
func TestDeleteQueueAndTheDeletedRecentlySwitch(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("again", nil)
		if err := h.deleteQueue(url); err != nil {
			h.Fatal(err.Message)
		}
		h.queue("again", nil)
	})
	runWith(t, sqs.Options{DeletedRecently: sqs.DeletedRecentlyEnforce}, func(h *harness) {
		url := h.queue("again", nil)
		if err := h.deleteQueue(url); err != nil {
			h.Fatal(err.Message)
		}
		_, err := h.create(&sqsapi.CreateQueueInput{QueueName: "again"})
		wantError(h.T, err, "AWS.SimpleQueueService.QueueDeletedRecently", "You must wait 60 seconds after deleting a queue before you can create another with the same name.")
		h.advance(time.Minute)
		h.queue("again", nil)
		if i := slices.IndexFunc(h.svc.Switches(), func(e fidelity.Entry) bool { return e.Name == "sqs.queueDeletedRecently" }); i < 0 || h.svc.Switches()[i].Value != "enforce" {
			h.Errorf("switches = %+v", h.svc.Switches())
		}
	})
}

// ARCHITECTURE.md principle 2: every behavior chosen without AWS-recorded
// evidence is a registered switch, named with its default value.
func TestSwitchesNameEveryUnrecordedChoice(t *testing.T) {
	run(t, func(h *harness) {
		got := map[string]string{}
		for _, e := range h.svc.Switches() {
			got[e.Name] = e.Value
		}
		want := map[string]string{
			"sqs.receipt.stale-handle":           "succeed-without-delete",
			"sqs.queueDeletedRecently":           "ignore",
			"sqs.move-task.message-identity":     "new-message",
			"sqs.visibility.ceiling-from":        "latest-receive",
			"sqs.fifo.receipt-expiry":            "delete-and-change-visibility",
			"sqs.fifo.duplicate":                 "original-message-id-and-sequence",
			"sqs.fifo.content-dedup-id":          "lowercase-hex-sha256",
			"sqs.fifo.sequence-number":           "20-digit-decimal-per-queue",
			"sqs.batch.unset-visibility-timeout": "zero",
			"sqs.policy.limits":                  "bytes-and-statements",
		}
		if !maps.Equal(got, want) {
			h.Errorf("switches = %v, want %v", got, want)
		}
	})
}

// A long poll on a queue that is deleted answers NonExistentQueue at once.
func TestDeleteQueueEndsItsLongPolls(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("doomed", nil)
		done := make(chan *awserr.Error, 1)
		go func() {
			_, err := h.receive(&sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(20))})
			done <- err
		}()
		synctest.Wait()
		if err := h.deleteQueue(url); err != nil {
			h.Fatal(err.Message)
		}
		wantError(h.T, <-done, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
	})
}

// sqs.md 5.3: PurgeQueue deletes delayed and in-flight messages too, and
// runs once per 60 s.
func TestPurgeQueue(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("purged", nil)
		h.mustSend(url, "in flight")
		h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		h.mustSend(url, "waiting")
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "delayed", DelaySeconds: new(int32(60))}); err != nil {
			h.Fatal(err.Message)
		}
		purge := func() *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpPurgeQueue, &sqsapi.PurgeQueueInput{QueueUrl: url})
			return err
		}
		if err := purge(); err != nil {
			h.Fatal(err.Message)
		}
		a := h.attributes(url, "All")
		if a["ApproximateNumberOfMessages"] != "0" || a["ApproximateNumberOfMessagesNotVisible"] != "0" || a["ApproximateNumberOfMessagesDelayed"] != "0" {
			h.Errorf("counts after a purge = %v", a)
		}
		err := purge()
		wantError(h.T, err, "AWS.SimpleQueueService.PurgeQueueInProgress", "Only one PurgeQueue operation on purged is allowed every 60 seconds.")
		if err.ID.Status != 403 {
			h.Errorf("status = %d, want 403", err.ID.Status)
		}
		h.advance(time.Minute)
		if err := purge(); err != nil {
			h.Errorf("a purge after 60 s = %s", err.Message)
		}
	})
}

// sqs.md 5.3: TagQueue overwrites by key, UntagQueue ignores unknown keys,
// ListQueueTags omits Tags when there are none, and 50 tags is the most.
func TestQueueTags(t *testing.T) {
	run(t, func(h *harness) {
		out, err := h.create(&sqsapi.CreateQueueInput{QueueName: "tagged", Tags: map[string]string{"team": "a", "Team": "b"}})
		if err != nil {
			h.Fatal(err.Message)
		}
		url := val(out.QueueUrl)
		tags := func() map[string]string {
			h.Helper()
			out, err := h.Invoke(h.Context(), sqsapi.OpListQueueTags, &sqsapi.ListQueueTagsInput{QueueUrl: url})
			if err != nil {
				h.Fatal(err.Message)
			}
			return out.Tags
		}
		if _, err := h.Invoke(h.Context(), sqsapi.OpTagQueue, &sqsapi.TagQueueInput{QueueUrl: url, Tags: map[string]string{"team": "c", "empty": ""}}); err != nil {
			h.Fatal(err.Message)
		}
		if got := tags(); !maps.Equal(got, map[string]string{"team": "c", "Team": "b", "empty": ""}) {
			h.Errorf("tags = %v", got)
		}
		if _, err := h.Invoke(h.Context(), sqsapi.OpUntagQueue, &sqsapi.UntagQueueInput{QueueUrl: url, TagKeys: []string{"team", "Team", "empty", "absent"}}); err != nil {
			h.Fatal(err.Message)
		}
		if got := tags(); got != nil {
			h.Errorf("tags after untagging all = %v, want no Tags", got)
		}
		many := map[string]string{}
		for i := range 51 {
			many["k"+strconv.Itoa(i)] = "v"
		}
		_, err = h.Invoke(h.Context(), sqsapi.OpTagQueue, &sqsapi.TagQueueInput{QueueUrl: url, Tags: many})
		wantError(h.T, err, "InvalidParameterValue", "Too many tags added for queue tagged.")
	})
}

// sqs.md 5.3: AddPermission writes the statement AWS recorded, a single
// principal or action as a string; RemovePermission takes it out and the
// last one removes the Policy attribute.
func TestPermissionsEditThePolicy(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("shared", nil)
		add := func(label string, accounts, actions []string) *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpAddPermission, &sqsapi.AddPermissionInput{QueueUrl: url, Label: label, AWSAccountIds: accounts, Actions: actions})
			return err
		}
		remove := func(label string) *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpRemovePermission, &sqsapi.RemovePermissionInput{QueueUrl: url, Label: label})
			return err
		}
		if err := add("crossaccountpermission", []string{"111111111111", "222222222222"}, []string{"ReceiveMessage"}); err != nil {
			h.Fatal(err.Message)
		}
		arn := arnPrefix + "shared"
		want := `{"Version":"2008-10-17","Id":"` + arn + `/SQSDefaultPolicy","Statement":[{"Sid":"crossaccountpermission","Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"]},"Action":"SQS:ReceiveMessage","Resource":"` + arn + `"}]}`
		if got := h.attributes(url, "Policy")["Policy"]; got != want {
			h.Fatalf("Policy = %s\nwant     %s", got, want)
		}
		wantError(h.T, add("crossaccountpermission", []string{"333333333333"}, []string{"SendMessage"}), "InvalidParameterValue", "Value crossaccountpermission for parameter Label is invalid. Reason: Already exists.")
		if err := add("second", []string{"333333333333"}, []string{"SendMessage", "DeleteMessage"}); err != nil {
			h.Fatal(err.Message)
		}
		wantError(h.T, add("many", []string{"333333333333"}, []string{"SendMessage", "ReceiveMessage", "DeleteMessage", "ChangeMessageVisibility", "GetQueueAttributes", "GetQueueUrl", "PurgeQueue", "TagQueue"}), "OverLimit", "8 Actions were found, maximum allowed is 7.")
		wantError(h.T, add("bad", []string{"333333333333"}, []string{"Fly"}), "InvalidParameterValue", "Value SQS:Fly for parameter ActionName is invalid. Reason: Please refer to the appropriate WSDL for a list of valid actions.")
		wantError(h.T, add("internal", []string{"333333333333"}, []string{"HoldVisibility"}), "InvalidParameterValue", "Value SQS:HoldVisibility for parameter ActionName is invalid. Reason: Please refer to the appropriate WSDL for a list of valid actions.")
		wantError(h.T, remove("crossaccountpermission2"), "InvalidParameterValue", "Value crossaccountpermission2 for parameter Label is invalid. Reason: can't find label.")
		for _, label := range []string{"crossaccountpermission", "second"} {
			if err := remove(label); err != nil {
				h.Fatal(err.Message)
			}
		}
		if _, ok := h.attributes(url, "All")["Policy"]; ok {
			h.Error("Policy is still reported after the last statement went")
		}
		// A statement joins a policy set by SetQueueAttributes, which keeps
		// its own members.
		own := `{"Sid":"own","Effect":"Allow","Principal":"*","Action":"SQS:SendMessage","Resource":"*"}`
		if err := h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"Policy": `{"Version":"2012-10-17","Statement":` + own + `}`}); err != nil {
			h.Fatal(err.Message)
		}
		if err := add("joined", []string{"111111111111"}, []string{"*"}); err != nil {
			h.Fatal(err.Message)
		}
		want = `{"Version":"2012-10-17","Statement":[` + own + `,{"Sid":"joined","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:root"},"Action":"SQS:*","Resource":"` + arn + `"}]}`
		if got := h.attributes(url, "Policy")["Policy"]; got != want {
			h.Errorf("Policy = %s\nwant     %s", got, want)
		}
		// AddPermission keeps the limits SetQueueAttributes enforces, so the
		// policy it leaves can be set back as read: 20 statements fit, a
		// 21st does not, and a refused statement changes nothing.
		for i := 3; i <= 20; i++ {
			if err := add("s"+strconv.Itoa(i), []string{"111111111111"}, []string{"SendMessage"}); err != nil {
				h.Fatalf("statement %d: %s", i, err.Message)
			}
		}
		full := h.attributes(url, "Policy")["Policy"]
		wantError(h.T, add("s21", []string{"111111111111"}, []string{"SendMessage"}), "OverLimit", "The permission cannot be added: the policy would have 21 statements; at most 20 are allowed.")
		if got := h.attributes(url, "Policy")["Policy"]; got != full {
			h.Errorf("a refused AddPermission changed the Policy to %s", got)
		}
		if err := h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"Policy": full}); err != nil {
			h.Errorf("setting the policy back as read = %s: %s", err.ID.QueryCode, err.Message)
		}
		// One label of 80 characters per statement passes 8,192 bytes
		// before 20 statements.
		long := h.queue("long", nil)
		var last *awserr.Error
		statements := 0
		for i := range 20 {
			label := strings.Repeat("x", 78) + strconv.Itoa(10+i)
			_, last = h.Invoke(h.Context(), sqsapi.OpAddPermission, &sqsapi.AddPermissionInput{QueueUrl: long, Label: label, AWSAccountIds: []string{"111111111111", "222222222222", "333333333333"}, Actions: []string{"SendMessage", "ReceiveMessage", "DeleteMessage", "ChangeMessageVisibility", "GetQueueAttributes", "GetQueueUrl", "PurgeQueue"}})
			if last != nil {
				break
			}
			statements++
		}
		if last == nil || last.ID.QueryCode != "OverLimit" || last.ID.Status != http.StatusForbidden || !strings.HasPrefix(last.Message, "The permission cannot be added: the policy would be ") || !strings.HasSuffix(last.Message, " bytes; at most 8192 are allowed.") {
			h.Fatalf("after %d long statements AddPermission = %v, want the size limit", statements, last)
		}
		if policy := h.attributes(long, "Policy")["Policy"]; len(policy) > 8192 {
			h.Errorf("stored Policy is %d bytes", len(policy))
		}
	})
}

// sqs.md 4: GetQueueAttributes answers what was named, everything for All
// and nothing for no name; a fresh standard queue reports exactly the
// recorded set.
func TestGetQueueAttributes(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("fresh", nil)
		got := slices.Sorted(maps.Keys(h.attributes(url, "All")))
		want := []sqsapi.QueueAttributeName{
			"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesDelayed", "ApproximateNumberOfMessagesNotVisible", "CreatedTimestamp", "DelaySeconds",
			"LastModifiedTimestamp", "MaximumMessageSize", "MessageRetentionPeriod", "QueueArn", "ReceiveMessageWaitTimeSeconds", "SqsManagedSseEnabled", "VisibilityTimeout",
		}
		if !slices.Equal(got, want) {
			h.Errorf("All = %v\nwant  %v", got, want)
		}
		if got := h.attributes(url); got != nil {
			h.Errorf("no names = %v, want nothing", got)
		}
		if got := h.attributes(url, "QueueArn", "FifoQueue"); !maps.Equal(got, map[sqsapi.QueueAttributeName]string{"QueueArn": arnPrefix + "fresh"}) {
			h.Errorf("QueueArn and FifoQueue on a standard queue = %v", got)
		}
		_, err := h.Invoke(h.Context(), sqsapi.OpGetQueueAttributes, &sqsapi.GetQueueAttributesInput{QueueUrl: url, AttributeNames: []sqsapi.QueueAttributeName{"Foobar"}})
		wantError(h.T, err, "InvalidAttributeName", "Unknown Attribute Foobar.")
	})
}

// sqs.md 4: SetQueueAttributes applies at once and refuses what cannot be
// set; "" clears a policy; a lower retention expires older messages.
func TestSetQueueAttributes(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("settings", map[sqsapi.QueueAttributeName]string{"Policy": `{"Version":"2012-10-17"}`})
		wantError(h.T, h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"FifoQueue": "true"}), "InvalidAttributeName", "Unknown Attribute FifoQueue.")
		wantError(h.T, h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"QueueArn": "x"}), "InvalidAttributeName", "Unknown Attribute QueueArn.")
		wantError(h.T, h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "43201"}), "InvalidAttributeValue", "Invalid value for the parameter VisibilityTimeout.")
		fifo := h.fifoQueue("settings.fifo", nil)
		wantError(h.T, h.setAttributes(fifo, map[sqsapi.QueueAttributeName]string{"FifoQueue": "false"}), "InvalidAttributeValue", "Invalid value for the parameter FifoQueue. Reason: Modifying queue type is not supported.")
		if err := h.setAttributes(fifo, map[sqsapi.QueueAttributeName]string{"FifoQueue": "true"}); err != nil {
			h.Errorf("FifoQueue=true on a FIFO queue = %s", err.Message)
		}

		created := h.attributes(url, "LastModifiedTimestamp")["LastModifiedTimestamp"]
		h.mustSend(url, "old")
		h.advance(2 * time.Minute)
		if err := h.setAttributes(url, map[sqsapi.QueueAttributeName]string{"Policy": "", "MessageRetentionPeriod": "60"}); err != nil {
			h.Fatal(err.Message)
		}
		synctest.Wait()
		a := h.attributes(url, "All")
		if _, ok := a["Policy"]; ok || a["MessageRetentionPeriod"] != "60" || a["LastModifiedTimestamp"] == created {
			h.Errorf("after SetQueueAttributes = %v", a)
		}
		if res := h.svc.Resources(h.Scope); !slices.ContainsFunc(res, func(r kernel.Resource) bool {
			return r.ARN.String() == arnPrefix+"settings" && r.Summary == "0 messages"
		}) {
			h.Errorf("a message past the lowered retention is still queued: %+v", res)
		}
	})
}

// sqs.md 7.3: retention removes a message on the emulator clock, in flight
// or not, and a delete with its handle afterwards still succeeds.
func TestRetentionRemovesInFlightMessages(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("retained", map[sqsapi.QueueAttributeName]string{"MessageRetentionPeriod": "60", "VisibilityTimeout": "600"})
		h.mustSend(url, "m")
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		h.advance(time.Minute)
		if a := h.attributes(url, "ApproximateNumberOfMessagesNotVisible"); a["ApproximateNumberOfMessagesNotVisible"] != "0" {
			h.Errorf("in flight after retention = %v", a)
		}
		h.mustDelete(url, &got[0])
	})
}

// sqs.md 7.2: ChangeMessageVisibility needs a current handle, makes the
// message receivable at 0, and stops at 12 hours from the receive.
func TestChangeMessageVisibility(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("vis", nil)
		h.mustSend(url, "m")
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		handle := val(got[0].ReceiptHandle)
		change := func(handle string, vt int32) *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: url, ReceiptHandle: handle, VisibilityTimeout: vt})
			return err
		}
		h.advance(time.Hour)
		wantError(h.T, change(handle, 43200), "InvalidParameterValue", "Value 43200 for parameter VisibilityTimeout is invalid. Reason: Total VisibilityTimeout for the message is beyond the limit [43200 seconds]")
		wantError(h.T, change(handle, 43201), "InvalidParameterValue", "Value 43201 for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200, if provided.")
		if err := change(handle, 0); err != nil {
			h.Fatal(err.Message)
		}
		again := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		if len(again) != 1 {
			h.Fatal("a zero visibility timeout did not make the message receivable")
		}
		wantError(h.T, change(handle, 10), "InvalidParameterValue", "Value "+handle+" for parameter ReceiptHandle is invalid. Reason: Message does not exist or is not available for visibility timeout change.")
	})
}

// HoldVisibility pins received messages past their timeout and releases
// them to it (ARCHITECTURE.md 9, ESM in-flight visibility).
func TestHoldVisibilityPinsAndReleases(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("held", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "5"})
		h.mustSend(url, "m")
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		hold := func(mode sqsapi.HoldVisibilityMode, handles ...string) *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpHoldVisibility, &sqsapi.HoldVisibilityInput{QueueUrl: url, ReceiptHandles: handles, Mode: mode})
			return err
		}
		if err := hold(sqsapi.HoldVisibilityModePin, val(got[0].ReceiptHandle)); err != nil {
			h.Fatal(err.Message)
		}
		h.advance(time.Minute)
		if again := h.receiveBodies(url, 1); len(again) != 0 {
			h.Fatalf("a pinned message was received: %q", again)
		}
		if visibilityTimers(h) != 0 {
			h.Errorf("a pinned message keeps %d visibility timers", visibilityTimers(h))
		}
		if err := hold(sqsapi.HoldVisibilityModeRelease, val(got[0].ReceiptHandle)); err != nil {
			h.Fatal(err.Message)
		}
		if again := h.receiveBodies(url, 1); len(again) != 1 {
			h.Fatal("a released message whose timeout passed is not receivable")
		}
		wantError(h.T, hold(sqsapi.HoldVisibilityModePin, "garbage"), "ReceiptHandleIsInvalid", `The input receipt handle "garbage" is not a valid receipt handle.`)
	})
}

// sqs.md 13.6: a URL's path names the account, so a URL of
// another account reaches that account's queue; the host is ignored, so a
// URL naming another region's host reaches the caller's region.
func TestQueueURLIdentifierForms(t *testing.T) {
	run(t, func(h *harness) {
		own := h.queue("forms", nil)
		h.Scope.Account = mustAccount(h.T, "111111111111")
		other := h.queue("forms", nil)
		h.Scope.Account = mustAccount(h.T, "000000000000")
		for _, tt := range []struct{ form, url string }{
			{"own account", own},
			{"another account", other},
			{"another region's host", "https://sqs.eu-west-1.amazonaws.com/000000000000/forms"},
		} {
			if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: tt.url, MessageBody: tt.form}); err != nil {
				h.Errorf("%s: SendMessage = %s: %s", tt.form, err.ID.QueryCode, err.Message)
			}
		}
		if got := h.attributes(own, "ApproximateNumberOfMessages")["ApproximateNumberOfMessages"]; got != "2" {
			h.Errorf("own queue holds %s messages, want 2 (own URL and the other host)", got)
		}
		if got := h.attributes(other, "ApproximateNumberOfMessages")["ApproximateNumberOfMessages"]; got != "1" {
			h.Errorf("the other account's queue holds %s messages, want 1", got)
		}
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: "http://localhost:4566/forms", MessageBody: "b"})
		wantError(h.T, err, "InvalidAddress", "The address http://localhost:4566/forms is not valid for this endpoint.")
	})
}
