package notify_test

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

const (
	queueARN    = "arn:aws:sqs:us-east-1:000000000000:q"
	topicARN    = "arn:aws:sns:us-east-1:000000000000:t"
	functionARN = "arn:aws:lambda:us-east-1:000000000000:function:f"
)

func filter(rules ...s3api.FilterRule) *s3api.NotificationConfigurationFilter {
	return &s3api.NotificationConfigurationFilter{Key: &s3api.S3KeyFilter{FilterRules: rules}}
}

func rule(name, value string) s3api.FilterRule {
	return s3api.FilterRule{Name: new(s3api.FilterRuleName(name)), Value: new(value)}
}

// fataler is the part of *testing.T and *rapid.T the helpers use.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func parse(t fataler, in s3api.NotificationConfiguration) notify.Config {
	t.Helper()
	c, err := notify.Parse("b", in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// Rule names are case-insensitive on input and returned capitalized; an
// empty value is accepted (AWS-recorded test_filter_rules_case_insensitive
// and test_filter_rules_empty_value).
func TestFilterRuleNamesAreReturnedCapitalized(t *testing.T) {
	c := parse(t, s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{
		Id: new("q1"), QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectcreated},
		Filter: filter(rule("PREFIX", "notif-"), rule("suffix", "")),
	}}})
	got := c.Wire().QueueConfigurations[0].Filter.Key.FilterRules
	want := []string{"Prefix=notif-", "Suffix="}
	if len(got) != len(want) {
		t.Fatalf("rules = %d, want %d", len(got), len(want))
	}
	for i, r := range got {
		if s := string(*r.Name) + "=" + *r.Value; s != want[i] {
			t.Errorf("rule %d = %s, want %s", i, s, want[i])
		}
	}
}

// A configuration without an Id gets one, which a second Parse of the
// stored configuration keeps.
func TestMissingIDIsGeneratedAndKept(t *testing.T) {
	in := s3api.NotificationConfiguration{
		QueueConfigurations: []s3api.QueueConfiguration{
			{QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectcreated}},
			{QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectremoved}},
		},
		TopicConfigurations: []s3api.TopicConfiguration{{Id: new("mine"), TopicArn: topicARN, Events: []s3api.Event{s3api.EventS3Objecttagging}}},
	}
	wire := parse(t, in).Wire()
	a, b := wire.QueueConfigurations[0].Id, wire.QueueConfigurations[1].Id
	if a == nil || b == nil || *a == "" || *a == *b {
		t.Fatalf("generated ids = %v, %v; want two distinct ids", a, b)
	}
	if got := *wire.TopicConfigurations[0].Id; got != "mine" {
		t.Fatalf("given id = %q, want mine", got)
	}
	again := parse(t, wire).Wire()
	if *again.QueueConfigurations[0].Id != *a {
		t.Fatalf("stored id changed on a second parse: %q, want %q", *again.QueueConfigurations[0].Id, *a)
	}
}

func TestParseRefusals(t *testing.T) {
	ev := []s3api.Event{s3api.EventS3Objectcreated}
	for name, tc := range map[string]struct {
		in   s3api.NotificationConfiguration
		want func(error) bool
	}{
		"malformed queue ARN": {
			s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: "not-an-arn", Events: ev}}},
			func(err error) bool {
				var e *notify.ARNError
				return errors.As(err, &e) && e.Kind == notify.Queue && e.Value == "not-an-arn"
			},
		},
		"topic ARN of another service": {
			s3api.NotificationConfiguration{TopicConfigurations: []s3api.TopicConfiguration{{TopicArn: queueARN, Events: ev}}},
			func(err error) bool {
				var e *notify.ARNError
				return errors.As(err, &e) && e.Kind == notify.Topic
			},
		},
		"malformed function ARN": {
			s3api.NotificationConfiguration{LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: "arn:aws:lambda", Events: ev}}},
			func(err error) bool {
				var e *notify.ARNError
				return errors.As(err, &e) && e.Kind == notify.Function
			},
		},
		"Lambda ARN that names no function": {
			s3api.NotificationConfiguration{LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: "arn:aws:lambda:us-east-1:000000000000:layer:x", Events: ev}}},
			func(err error) bool {
				var e *notify.ARNError
				return errors.As(err, &e) && e.Kind == notify.Function && e.Value == "arn:aws:lambda:us-east-1:000000000000:layer:x"
			},
		},
		"unknown event": {
			s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: []s3api.Event{"s3:Nope"}}}},
			func(err error) bool {
				var e *notify.EventError
				return errors.As(err, &e) && e.Event == "s3:Nope"
			},
		},
		"filter rule name": {
			s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: ev, Filter: filter(rule("middle", "x"))}}},
			func(err error) bool {
				var e *notify.FilterNameError
				return errors.As(err, &e) && e.Name == "middle"
			},
		},
		"filter rule without a value": {
			s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: ev, Filter: filter(s3api.FilterRule{Name: new(s3api.FilterRuleNamePrefix)})}}},
			func(err error) bool {
				var e *notify.FilterRuleError
				return errors.As(err, &e)
			},
		},
		"two prefix rules": {
			s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: ev, Filter: filter(rule("prefix", "a"), rule("Prefix", "b"))}}},
			func(err error) bool {
				var e *notify.RepeatedFilterError
				return errors.As(err, &e) && e.Part == notify.Prefix
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := notify.Parse("b", tc.in)
			if !tc.want(err) {
				t.Fatalf("Parse error = %v (%T)", err, err)
			}
		})
	}
}

// A put selected by a queue, a topic or a function configuration raises
// one delivery to it: SendMessage of the record, Publish with S3's
// subject, or an asynchronous Invoke, all with S3's retry policy and the
// bucket as source.
func TestPutRaisesOneDeliveryToTheSelectingConfiguration(t *testing.T) {
	all := []s3api.Event{s3api.EventS3Objectcreated}
	c := parse(t, s3api.NotificationConfiguration{
		TopicConfigurations:          []s3api.TopicConfiguration{{Id: new("t"), TopicArn: topicARN, Events: all, Filter: filter(rule("prefix", "t/"))}},
		QueueConfigurations:          []s3api.QueueConfiguration{{Id: new("q"), QueueArn: queueARN, Events: all, Filter: filter(rule("prefix", "q/"))}},
		LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{Id: new("f"), LambdaFunctionArn: functionARN, Events: all, Filter: filter(rule("prefix", "f/"))}},
	})
	deliver := func(key string, want delivery.Mechanism) delivery.Target {
		t.Helper()
		ev := documented(notify.Created{Action: notify.CreatedPut, Size: 3, ETag: "e", Sequencer: 1})
		ev.Key = key
		got, err := c.Requests(ev)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("a put of %s raised %d deliveries, want 1", key, len(got))
		}
		r := got[0]
		if r.Mechanism != want || r.Source != ev.Bucket.ARN || r.Policy != (delivery.S3Notification{}) || r.DLQ != nil {
			t.Fatalf("delivery of %s = %+v, want %s from the bucket under S3Notification", key, r, want)
		}
		return r.Target
	}
	topic, ok := deliver("t/a", delivery.S3ToSNS).(delivery.SNSTopic)
	if !ok || topic.ARN.String() != topicARN || topic.Subject != "Amazon S3 Notification" || !strings.Contains(topic.Message, `"configurationId":"t"`) {
		t.Fatalf("topic target = %+v", topic)
	}
	queue, ok := deliver("q/a", delivery.S3ToSQS).(delivery.SQSQueue)
	if !ok || queue.ARN.String() != queueARN || !strings.Contains(queue.Body, `"configurationId":"q"`) {
		t.Fatalf("queue target = %+v", queue)
	}
	fn, ok := deliver("f/a", delivery.S3ToLambda).(delivery.LambdaFunction)
	if !ok || fn.ARN.String() != functionARN || fn.Invocation != delivery.InvocationEvent || !strings.Contains(string(fn.Event), `"configurationId":"f"`) {
		t.Fatalf("function target = %+v", fn)
	}
}

// A rule built by hand with a destination kind outside the declared ones
// raises an error, not a delivery with no mechanism.
func TestUnknownKindRaisesNoDelivery(t *testing.T) {
	arn, err := aws.ParseARN(queueARN)
	if err != nil {
		t.Fatal(err)
	}
	c := notify.Config{Rules: []notify.Rule{{ID: "r", ARN: arn, Events: []s3api.Event{s3api.EventS3Objectcreated}}}}
	_, err = c.Requests(documented(notify.Created{Action: notify.CreatedPut}))
	var e *notify.KindError
	if !errors.As(err, &e) || e.Kind != 0 {
		t.Fatalf("Requests = %v, want a KindError naming Kind(0)", err)
	}
}

// A change that is nil or has an undeclared action names no event S3
// reports, so even a wildcard rule raises nothing and Requests says why.
func TestUnknownChangeRaisesNoDelivery(t *testing.T) {
	c := parse(t, s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectcreated}}}})
	for name, change := range map[string]notify.Change{"zero action": notify.Created{}, "unknown action": notify.Created{Action: 9}, "nil": nil} {
		got, err := c.Requests(documented(change))
		var e *notify.ChangeError
		if !errors.As(err, &e) || e.Change != change || len(got) != 0 {
			t.Errorf("%s: Requests = %d deliveries, %v; want none and a ChangeError", name, len(got), err)
		}
	}
}

// The test event goes to queues and topics; a function gets none.
func TestTestEventGoesToQueuesAndTopicsOnly(t *testing.T) {
	c := parse(t, s3api.NotificationConfiguration{
		QueueConfigurations:          []s3api.QueueConfiguration{{QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectcreated}}},
		TopicConfigurations:          []s3api.TopicConfiguration{{TopicArn: topicARN, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
		LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: functionARN, Events: []s3api.Event{s3api.EventS3Objecttagging}}},
	})
	b := documented(nil).Bucket
	got, err := c.TestRequests(b, time.Unix(0, 0), notify.Request{ID: "R", HostID: "H"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Mechanism != delivery.S3ToSNS || got[1].Mechanism != delivery.S3ToSQS {
		t.Fatalf("test deliveries = %+v, want the topic then the queue", got)
	}
	if q, ok := got[1].Target.(delivery.SQSQueue); !ok || !strings.Contains(q.Body, `"Event":"s3:TestEvent"`) {
		t.Fatalf("queue target = %+v, want the test event", got[1].Target)
	}
}

// An event type selects the change it names, a wildcard every change of
// its family, and nothing else.
func TestEventTypesSelectTheirChanges(t *testing.T) {
	for _, tc := range []struct {
		event  s3api.Event
		change notify.Change
		want   bool
	}{
		{s3api.EventS3Objectcreated, notify.Created{Action: notify.CreatedCopy}, true},
		{s3api.EventS3Objectcreated, notify.Removed{Action: notify.RemovedDelete}, false},
		{s3api.EventS3ObjectcreatedPut, notify.Created{Action: notify.CreatedPut}, true},
		{s3api.EventS3ObjectcreatedPut, notify.Created{Action: notify.CreatedCopy}, false},
		{s3api.EventS3Objectremoved, notify.Removed{Action: notify.RemovedDeleteMarkerCreated}, true},
		{s3api.EventS3ObjectremovedDelete, notify.Removed{Action: notify.RemovedDeleteMarkerCreated}, false},
		{s3api.EventS3Objecttagging, notify.Tagged{Action: notify.TaggingDelete}, true},
		{s3api.EventS3Objecttagging, notify.Created{Action: notify.CreatedPut}, false},
	} {
		c := parse(t, s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: queueARN, Events: []s3api.Event{tc.event}}}})
		got, err := c.Requests(documented(tc.change))
		if err != nil {
			t.Fatal(err)
		}
		if (len(got) == 1) != tc.want {
			t.Errorf("%s selects %s = %v, want %v", tc.event, tc.change.Name(), len(got) == 1, tc.want)
		}
	}
}

// text draws prefix, suffix and key text of up to maxLen bytes from
// alphabet, a regexp character class body. The alphabets are small, so
// draws often share a prefix or a suffix.
func text(alphabet string, maxLen int) *rapid.Generator[string] {
	return rapid.StringMatching(`[` + alphabet + `]{0,` + strconv.Itoa(maxLen) + `}`)
}

// keyText is raw key text: a space encodes to '+' and a '+' to %2B, so the
// encoded and the raw key differ.
func keyText(maxLen int) *rapid.Generator[string] { return text(`ab/ +`, maxLen) }

// drawFilter draws an optional prefix rule and an optional suffix rule,
// their values from values.
func drawFilter(t *rapid.T, label string, values *rapid.Generator[string]) (prefix, suffix *string, f *s3api.NotificationConfigurationFilter) {
	var rules []s3api.FilterRule
	if rapid.Bool().Draw(t, label+" has prefix") {
		prefix = new(values.Draw(t, label+" prefix"))
		rules = append(rules, rule("prefix", *prefix))
	}
	if rapid.Bool().Draw(t, label+" has suffix") {
		suffix = new(values.Draw(t, label+" suffix"))
		rules = append(rules, rule("Suffix", *suffix))
	}
	if len(rules) == 0 {
		return prefix, suffix, nil
	}
	return prefix, suffix, filter(rules...)
}

// putSelects reports whether the single-rule configuration with filter f
// selects a put of key.
func putSelects(t fataler, f *s3api.NotificationConfigurationFilter, key string) bool {
	t.Helper()
	c := parse(t, s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{
		QueueArn: queueARN, Events: []s3api.Event{s3api.EventS3Objectcreated}, Filter: f,
	}}})
	ev := documented(notify.Created{Action: notify.CreatedPut})
	ev.Key = key
	got, err := c.Requests(ev)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	return len(got) == 1
}

// A filter selects exactly the keys whose URL-encoded form its prefix and
// suffix both match (notification-how-to-filtering.html).
func TestFiltersSelectExactlyTheMatchingKeys(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix, suffix, f := drawFilter(t, "rule", keyText(3))
		key := keyText(6).Draw(t, "key")
		encoded := notify.EncodeKey(key)
		want := (prefix == nil || strings.HasPrefix(encoded, *prefix)) && (suffix == nil || strings.HasSuffix(encoded, *suffix))
		if got := putSelects(t, f, key); got != want {
			t.Fatalf("key %q (encoded %q) selected = %v, want %v", key, encoded, got, want)
		}
	})
}

// A filter value names a space as '+' and other special characters
// URL-encoded, as the key appears in the record.
func TestFiltersMatchTheEncodedKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule s3api.FilterRule
		key  string
		want bool
	}{
		{"prefix with '+' selects a space", rule("prefix", "a+b"), "a b", true},
		{"prefix with a space selects nothing", rule("prefix", "a b"), "a b", false},
		{"suffix with %40 selects '@'", rule("suffix", "%40x"), "@x", true},
		{"suffix with a raw '@' selects nothing", rule("suffix", "@x"), "@x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := putSelects(t, filter(tc.rule), tc.key); got != tc.want {
				t.Fatalf("%s %q selects %q = %v, want %v", *tc.rule.Name, *tc.rule.Value, tc.key, got, tc.want)
			}
		})
	}
}

// overlapEvents are the event types the overlap property draws from: two
// wildcards, the changes they cover, and a family neither covers.
var overlapEvents = []s3api.Event{
	s3api.EventS3Objectcreated, s3api.EventS3ObjectcreatedPut, s3api.EventS3ObjectcreatedCopy,
	s3api.EventS3Objectremoved, s3api.EventS3ObjectremovedDelete, s3api.EventS3ObjecttaggingPut,
}

// changes are the object changes the overlap property tries.
var changes = []notify.Change{
	notify.Created{Action: notify.CreatedPut},
	notify.Created{Action: notify.CreatedCopy},
	notify.Created{Action: notify.CreatedPost},
	notify.Removed{Action: notify.RemovedDelete},
	notify.Removed{Action: notify.RemovedDeleteMarkerCreated},
	notify.Tagged{Action: notify.TaggingPut},
}

// Parse accepts a configuration only when no change of any key selects
// two of its rules, and the pair an OverlapError names is selected by one
// change of the key that starts with the longer prefix and ends with the
// longer suffix.
//
// Filter values are drawn in encoded form ('+' for a space), so every
// witness has a raw key that encodes to it.
func TestParseRefusesExactlyOverlappingRules(t *testing.T) {
	type drawn struct {
		config         s3api.QueueConfiguration
		prefix, suffix string // "" when absent
	}
	// witness is the raw key that starts with the longer prefix and ends
	// with the longer suffix of a and b: the one key a pair that some key
	// satisfies both of surely selects.
	witness := func(a, b drawn) string {
		return strings.ReplaceAll(longer(a.prefix, b.prefix)+longer(a.suffix, b.suffix), "+", " ")
	}
	rapid.Check(t, func(t *rapid.T) {
		var in s3api.NotificationConfiguration
		rules := map[string]drawn{}
		for i := range rapid.IntRange(1, 4).Draw(t, "rules") {
			id := string(rune('A' + i))
			prefix, suffix, f := drawFilter(t, id, text(`ab/+`, 3))
			events := rapid.SliceOfNDistinct(rapid.SampledFrom(overlapEvents), 1, 2, rapid.ID).Draw(t, id+" events")
			q := s3api.QueueConfiguration{Id: new(id), QueueArn: queueARN, Events: events, Filter: f}
			in.QueueConfigurations = append(in.QueueConfigurations, q)
			rules[id] = drawn{config: q, prefix: valueOr(prefix), suffix: valueOr(suffix)}
		}
		keys := rapid.SliceOfN(keyText(6), 1, 8).Draw(t, "keys")
		for i, a := range in.QueueConfigurations {
			for _, b := range in.QueueConfigurations[i+1:] {
				keys = append(keys, witness(rules[*a.Id], rules[*b.Id]))
			}
		}
		c, err := notify.Parse("b", in)
		if err == nil {
			// The stored form reads back to the configuration Parse accepted.
			if stored, err := notify.ParseStored("b", c.Wire()); err != nil || !reflect.DeepEqual(stored, c) {
				t.Fatalf("ParseStored(Wire) = %+v, %v; want %+v", stored, err, c)
			}
			for _, key := range keys {
				for _, ch := range changes {
					if n := selected(t, c, ch, key); n > 1 {
						t.Fatalf("accepted configuration: %s of %q selects %d rules", ch.Name(), key, n)
					}
				}
			}
			return
		}
		var overlap *notify.OverlapError
		if !errors.As(err, &overlap) {
			t.Fatalf("Parse = %v, want an OverlapError or nothing", err)
		}
		a, b := rules[overlap.First], rules[overlap.Second]
		key := witness(a, b)
		alone := func(d drawn) notify.Config {
			return parse(t, s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{d.config}})
		}
		if !slices.ContainsFunc(changes, func(ch notify.Change) bool {
			return selected(t, alone(a), ch, key) == 1 && selected(t, alone(b), ch, key) == 1
		}) {
			t.Fatalf("Parse refused %s and %s, but no change of %q selects both", overlap.First, overlap.Second, key)
		}
		if same := a.prefix == b.prefix && a.suffix == b.suffix; same != (overlap.Overlap == notify.SameFilter) {
			t.Fatalf("overlap = %d for filters %q/%q and %q/%q", overlap.Overlap, a.prefix, a.suffix, b.prefix, b.suffix)
		}
	})
}

// The documented examples (notification-how-to-filtering.html): overlapping
// filters for different event types, and a common prefix with disjoint
// suffixes, are valid; an unfiltered rule beside a filtered one, suffixes
// one of which ends the other, and a common suffix under a wildcard and
// one of its events are not.
func TestParseFollowsTheDocumentedOverlapExamples(t *testing.T) {
	put, removed := []s3api.Event{s3api.EventS3ObjectcreatedPut}, []s3api.Event{s3api.EventS3Objectremoved}
	created := []s3api.Event{s3api.EventS3Objectcreated}
	for name, tc := range map[string]struct {
		rules []s3api.TopicConfiguration
		want  notify.Overlap // zero when valid
	}{
		"same prefix, different event types": {rules: []s3api.TopicConfiguration{
			{TopicArn: topicARN, Events: put, Filter: filter(rule("prefix", "image/"))},
			{TopicArn: topicARN, Events: removed, Filter: filter(rule("prefix", "image/"))},
		}},
		"common prefix, disjoint suffixes": {rules: []s3api.TopicConfiguration{
			{TopicArn: topicARN, Events: put, Filter: filter(rule("prefix", "images"), rule("suffix", ".jpg"))},
			{TopicArn: topicARN, Events: put, Filter: filter(rule("prefix", "images"), rule("suffix", ".png"))},
		}},
		"unfiltered beside a prefix": {rules: []s3api.TopicConfiguration{
			{TopicArn: topicARN, Events: created},
			{TopicArn: topicARN, Events: created, Filter: filter(rule("prefix", "images"))},
		}, want: notify.OverlappingFilter},
		"suffixes jpg and pg": {rules: []s3api.TopicConfiguration{
			{TopicArn: topicARN, Events: created, Filter: filter(rule("suffix", "jpg"))},
			{TopicArn: topicARN, Events: put, Filter: filter(rule("suffix", "pg"))},
		}, want: notify.OverlappingFilter},
		"both unfiltered": {rules: []s3api.TopicConfiguration{
			{TopicArn: topicARN, Events: created},
			{TopicArn: topicARN, Events: put},
		}, want: notify.SameFilter},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := notify.Parse("b", s3api.NotificationConfiguration{TopicConfigurations: tc.rules})
			var overlap *notify.OverlapError
			switch {
			case tc.want == 0 && err != nil:
				t.Fatalf("Parse = %v, want the configuration accepted", err)
			case tc.want != 0 && (!errors.As(err, &overlap) || overlap.Overlap != tc.want):
				t.Fatalf("Parse = %v, want an OverlapError of kind %d", err, tc.want)
			}
		})
	}
}

// selected is the number of c's rules that ch of key selects.
func selected(t *rapid.T, c notify.Config, ch notify.Change, key string) int {
	ev := documented(ch)
	ev.Key = key
	got, err := c.Requests(ev)
	if err != nil {
		t.Fatal(err)
	}
	return len(got)
}

func valueOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func longer(a, b string) string {
	if len(a) >= len(b) {
		return a
	}
	return b
}

// A stored configuration with only EventBridge enabled is not empty.
func TestEventBridgeIsKept(t *testing.T) {
	c := parse(t, s3api.NotificationConfiguration{EventBridgeConfiguration: &s3api.EventBridgeConfiguration{}})
	if c.Empty() || c.Wire().EventBridgeConfiguration == nil {
		t.Fatalf("EventBridge configuration was dropped: %+v", c)
	}
	if !parse(t, s3api.NotificationConfiguration{}).Empty() {
		t.Fatal("the empty configuration is not Empty")
	}
}
