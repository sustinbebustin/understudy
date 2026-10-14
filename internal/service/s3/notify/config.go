package notify

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// Config is a bucket's parsed notification configuration: its rules in
// document order (topics, queues, then functions), which is the fan-out
// order of a change. The s3 package stores its Wire form and reads it back
// with ParseStored for each object change.
type Config struct {
	Rules []Rule
	// EventBridge is an EventBridgeConfiguration element: stored and read
	// back, but no event is sent to EventBridge.
	EventBridge bool
}

// Rule is one topic, queue or function configuration.
type Rule struct {
	ID     string
	Kind   Kind
	ARN    aws.ARN
	Events []s3api.Event
	// Filter holds the key filter rules in the order given.
	Filter []KeyRule
}

// KeyRule is one Filter/S3Key/FilterRule.
type KeyRule struct {
	Part  Part
	Value string
}

// Kind is the destination of a rule.
type Kind uint8

// Kind values.
const (
	Queue Kind = iota + 1
	Topic
	Function
)

// Element is the XML element naming the destination ARN, which
// InvalidArgument names as ArgumentName: Queue, Topic or CloudFunction.
func (k Kind) Element() string {
	switch k {
	case Queue:
		return "Queue"
	case Topic:
		return "Topic"
	case Function:
		return "CloudFunction"
	}
	return fmt.Sprintf("Kind(%d)", uint8(k))
}

// KindError is a rule whose Kind is outside the declared values: a Rule
// built by hand rather than by Parse, which is a bug in its builder.
type KindError struct {
	Kind Kind
}

func (e *KindError) Error() string {
	return fmt.Sprintf("s3 notify: the rule's destination kind %s is not Queue, Topic or CloudFunction, so it has no delivery path; build rules with Parse", e.Kind.Element())
}

// ChangeError is an event whose Change is nil or whose action is outside
// the declared values, so it names no event S3 reports: a Change built by
// hand with a zero or unknown action, which is a bug in its builder.
type ChangeError struct {
	// Change is the refused change; nil when the event carried none.
	Change Change
}

func (e *ChangeError) Error() string {
	if e.Change == nil {
		return "s3 notify: the event carries no change, so it names no event type; set Change to a Created, Removed or Tagged value"
	}
	return fmt.Sprintf("s3 notify: the change %q names no event S3 reports, as its action is outside the declared values; build it with a declared action", e.Change.Name())
}

// checkChange refuses a change that is nil or names no event type of the
// model's Event enum.
func checkChange(c Change) error {
	if c == nil || !s3api.Event("s3:"+c.Name()).Known() {
		return &ChangeError{Change: c}
	}
	return nil
}

// service is the ARN service a destination of k names.
func (k Kind) service() string {
	switch k {
	case Queue:
		return "sqs"
	case Topic:
		return "sns"
	case Function:
		return "lambda"
	}
	return ""
}

// names reports whether a is an ARN of a destination of k: of k's
// service, and for a function a function resource ("function:name", with
// an optional qualifier), not a layer or an event source mapping.
func (k Kind) names(a aws.ARN) bool {
	if a.Service != k.service() {
		return false
	}
	if k != Function {
		return true
	}
	name, ok := strings.CutPrefix(a.Resource, "function:")
	return ok && name != ""
}

// Part is what a key filter rule compares.
type Part uint8

// Part values.
const (
	Prefix Part = iota + 1
	Suffix
)

// String is the model's rule name: "prefix" or "suffix"; a value outside
// the declared ones reads "Part(n)".
func (p Part) String() string {
	switch p {
	case Prefix:
		return string(s3api.FilterRuleNamePrefix)
	case Suffix:
		return string(s3api.FilterRuleNameSuffix)
	}
	return fmt.Sprintf("Part(%d)", uint8(p))
}

// wire is the rule name GetBucketNotificationConfiguration returns,
// capitalized (AWS-recorded test_filter_rules_case_insensitive); a value
// outside the declared ones reads "Part(n)".
func (p Part) wire() s3api.FilterRuleName {
	switch p {
	case Prefix:
		return "Prefix"
	case Suffix:
		return "Suffix"
	}
	return s3api.FilterRuleName(p.String())
}

// ARNError is a destination ARN that does not parse as an ARN of the
// destination's service: S3's "The ARN could not be parsed".
type ARNError struct {
	Kind  Kind
	Value string
}

func (e *ARNError) Error() string {
	return fmt.Sprintf("the %s ARN %q could not be parsed as an ARN of %s", e.Kind.Element(), e.Value, e.Kind.service())
}

// EventError is an event type outside the model's Event enum.
type EventError struct {
	Event string
}

func (e *EventError) Error() string {
	return fmt.Sprintf("the event %q is not supported for notifications", e.Event)
}

// FilterNameError is a filter rule named other than prefix or suffix.
type FilterNameError struct {
	Name string
}

func (e *FilterNameError) Error() string {
	return fmt.Sprintf("filter rule name %q must be either prefix or suffix", e.Name)
}

// FilterRuleError is a filter rule missing its Name or its Value, which S3
// answers as MalformedXML.
type FilterRuleError struct{}

func (*FilterRuleError) Error() string { return "a filter rule needs both a Name and a Value" }

// RepeatedFilterError is a filter with two prefix or two suffix rules.
type RepeatedFilterError struct {
	Part Part
}

func (e *RepeatedFilterError) Error() string {
	return fmt.Sprintf("a filter holds more than one %s rule", e.Part)
}

// RegionError is a destination outside the bucket's home region.
type RegionError struct {
	Kind   Kind
	ARN    string
	Region string
}

func (e *RegionError) Error() string {
	return fmt.Sprintf("the %s %s is outside the bucket's region %s; S3 notifies only destinations in the bucket's region", e.Kind.Element(), e.ARN, e.Region)
}

// Parse reads the configuration of bucket: it checks every destination ARN,
// event type and filter rule, refuses two rules that one change of one key
// would select both of, capitalizes rule names and gives each
// configuration without an Id a generated one. Parsing a stored
// configuration again gives the same Config.
func Parse(bucket string, in s3api.NotificationConfiguration) (Config, error) {
	c, err := parseRules(bucket, in)
	if err != nil {
		return Config{}, err
	}
	if err := checkOverlaps(c.Rules); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ParseStored reads a configuration Parse accepted and Wire stored. It
// skips the pairwise overlap check, which held when the configuration was
// stored and which no later change can break, so an object change does not
// pay for it. An error here means the stored form no longer parses: a bug
// in the code that stored it.
func ParseStored(bucket string, in s3api.NotificationConfiguration) (Config, error) {
	return parseRules(bucket, in)
}

// parseRules is Parse without the overlap check.
func parseRules(bucket string, in s3api.NotificationConfiguration) (Config, error) {
	c := Config{EventBridge: in.EventBridgeConfiguration != nil}
	add := func(kind Kind, id *string, arn string, events []s3api.Event, f *s3api.NotificationConfigurationFilter) error {
		r, err := parseRule(bucket, len(c.Rules), kind, id, arn, events, f)
		if err != nil {
			return err
		}
		c.Rules = append(c.Rules, r)
		return nil
	}
	for _, t := range in.TopicConfigurations {
		if err := add(Topic, t.Id, t.TopicArn, t.Events, t.Filter); err != nil {
			return Config{}, err
		}
	}
	for _, q := range in.QueueConfigurations {
		if err := add(Queue, q.Id, q.QueueArn, q.Events, q.Filter); err != nil {
			return Config{}, err
		}
	}
	for _, f := range in.LambdaFunctionConfigurations {
		if err := add(Function, f.Id, f.LambdaFunctionArn, f.Events, f.Filter); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// Overlap is how two rules that select a common change share a key.
type Overlap uint8

// Overlap values.
const (
	// SameFilter is two rules with the same prefix and the same suffix, two
	// unfiltered rules included.
	SameFilter Overlap = iota + 1
	// OverlappingFilter is two rules with different filters that some key
	// still satisfies both of.
	OverlappingFilter
)

// OverlapError is two rules that some change of some key selects both of:
// their event types share a change (a wildcard covers its family) and one
// prefix starts the other and one suffix ends the other, an absent filter
// rule counting as the empty one. S3 refuses the configuration, so every
// change selects at most one rule (notification-how-to-filtering.html).
type OverlapError struct {
	// First and Second are the Ids of the two rules, in document order.
	First, Second string
	Overlap       Overlap
}

func (e *OverlapError) Error() string {
	return fmt.Sprintf("the configurations %q and %q select a common event type for a common key; give them disjoint event types or non-overlapping prefix or suffix filters", e.First, e.Second)
}

// checkOverlaps refuses the first pair of rules, in document order, that
// a change of some key selects both of.
func checkOverlaps(rules []Rule) error {
	for i := range rules {
		for j := i + 1; j < len(rules); j++ {
			a, b := &rules[i], &rules[j]
			if !eventsMeet(a.Events, b.Events) {
				continue
			}
			ap, as := filterValue(a, Prefix), filterValue(a, Suffix)
			bp, bs := filterValue(b, Prefix), filterValue(b, Suffix)
			if !overlapping(ap, bp, strings.HasPrefix) || !overlapping(as, bs, strings.HasSuffix) {
				continue
			}
			o := OverlappingFilter
			if ap == bp && as == bs {
				o = SameFilter
			}
			return &OverlapError{First: a.ID, Second: b.ID, Overlap: o}
		}
	}
	return nil
}

// overlapping reports whether some key satisfies both filter values: one
// starts (or ends) the other.
func overlapping(x, y string, has func(s, part string) bool) bool {
	return has(x, y) || has(y, x)
}

// eventsMeet reports whether some change is named by an event type of a
// and one of b.
func eventsMeet(a, b []s3api.Event) bool {
	for _, x := range a {
		for _, y := range b {
			if covers(x, y) || covers(y, x) {
				return true
			}
		}
	}
	return false
}

// covers reports whether every change y names is one x names: x is y, or
// x is the wildcard of y's family ("s3:ObjectCreated:*" covers
// "s3:ObjectCreated:Put").
func covers(x, y s3api.Event) bool {
	family, wildcard := strings.CutSuffix(string(x), "*")
	return x == y || (wildcard && strings.HasSuffix(family, ":") && strings.HasPrefix(string(y), family))
}

// filterValue is the value of the rule's part filter, or "" when it has
// none: an absent rule selects every key, as the empty one does.
func filterValue(r *Rule, p Part) string {
	for _, k := range r.Filter {
		if k.Part == p {
			return k.Value
		}
	}
	return ""
}

func parseRule(bucket string, index int, kind Kind, id *string, arn string, events []s3api.Event, f *s3api.NotificationConfigurationFilter) (Rule, error) {
	a, err := aws.ParseARN(arn)
	if err != nil || !kind.names(a) {
		return Rule{}, &ARNError{Kind: kind, Value: arn}
	}
	for _, e := range events {
		if !e.Known() {
			return Rule{}, &EventError{Event: string(e)}
		}
	}
	filter, err := parseFilter(f)
	if err != nil {
		return Rule{}, err
	}
	r := Rule{Kind: kind, ARN: a, Events: events, Filter: filter}
	if id != nil && *id != "" {
		r.ID = *id
	} else {
		r.ID = generatedID(bucket, index, kind, arn)
	}
	return r, nil
}

func parseFilter(f *s3api.NotificationConfigurationFilter) ([]KeyRule, error) {
	if f == nil || f.Key == nil {
		return nil, nil
	}
	var out []KeyRule
	for _, r := range f.Key.FilterRules {
		if r.Name == nil || r.Value == nil {
			return nil, &FilterRuleError{}
		}
		var part Part
		switch strings.ToLower(string(*r.Name)) {
		case string(s3api.FilterRuleNamePrefix):
			part = Prefix
		case string(s3api.FilterRuleNameSuffix):
			part = Suffix
		default:
			return nil, &FilterNameError{Name: string(*r.Name)}
		}
		for _, seen := range out {
			if seen.Part == part {
				return nil, &RepeatedFilterError{Part: part}
			}
		}
		out = append(out, KeyRule{Part: part, Value: *r.Value})
	}
	return out, nil
}

// generatedID is the Id of a configuration sent without one: the base64 of
// a UUID-shaped digest of the bucket, the configuration's position and its
// destination, the shape AWS's generated ids have. It is a pure function,
// so the snapshot of a seeded run does not depend on it drawing ids.
func generatedID(bucket string, index int, kind Kind, arn string) string {
	sum := sha256.Sum256([]byte(bucket + "\x00" + strconv.Itoa(index) + "\x00" + kind.Element() + "\x00" + arn))
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	return base64.StdEncoding.EncodeToString([]byte(uuid))
}

// Empty reports a configuration that configures nothing: the one an empty
// <NotificationConfiguration/> sends to turn notifications off.
func (c Config) Empty() bool { return len(c.Rules) == 0 && !c.EventBridge }

// Wire is the configuration as S3 stores and returns it.
func (c Config) Wire() s3api.NotificationConfiguration {
	var out s3api.NotificationConfiguration
	if c.EventBridge {
		out.EventBridgeConfiguration = &s3api.EventBridgeConfiguration{}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		id, arn, f := new(r.ID), r.ARN.String(), r.wireFilter()
		switch r.Kind {
		case Topic:
			out.TopicConfigurations = append(out.TopicConfigurations, s3api.TopicConfiguration{Id: id, TopicArn: arn, Events: r.Events, Filter: f})
		case Queue:
			out.QueueConfigurations = append(out.QueueConfigurations, s3api.QueueConfiguration{Id: id, QueueArn: arn, Events: r.Events, Filter: f})
		case Function:
			out.LambdaFunctionConfigurations = append(out.LambdaFunctionConfigurations, s3api.LambdaFunctionConfiguration{Id: id, LambdaFunctionArn: arn, Events: r.Events, Filter: f})
		}
	}
	return out
}

// InRegion refuses the first rule, in document order, whose destination is
// outside region, the bucket's home region: S3 delivers only within the
// bucket's region (notification-how-to-event-types-and-destinations.html).
func (c Config) InRegion(region string) error {
	for i := range c.Rules {
		if r := &c.Rules[i]; r.ARN.Region != region {
			return &RegionError{Kind: r.Kind, ARN: r.ARN.String(), Region: region}
		}
	}
	return nil
}

// selects reports whether the rule reports change to key: an event type
// names the change or its family's wildcard, and the URL-encoded key, as
// EncodeKey gives it, satisfies both filter rules. A filter value names a
// space as '+' and other special characters as %XX
// (notification-how-to-filtering.html), so "a+b" selects the key "a b" and
// "a b" selects no key. change must have passed checkChange.
func (r *Rule) selects(change Change, key string) bool {
	name := change.Name()
	family, _, _ := strings.Cut(name, ":")
	if !slices.ContainsFunc(r.Events, func(e s3api.Event) bool {
		return string(e) == "s3:"+name || string(e) == "s3:"+family+":*"
	}) {
		return false
	}
	encoded := EncodeKey(key)
	for _, k := range r.Filter {
		if (k.Part == Prefix && !strings.HasPrefix(encoded, k.Value)) || (k.Part == Suffix && !strings.HasSuffix(encoded, k.Value)) {
			return false
		}
	}
	return true
}

func (r *Rule) wireFilter() *s3api.NotificationConfigurationFilter {
	if len(r.Filter) == 0 {
		return nil
	}
	rules := make([]s3api.FilterRule, len(r.Filter))
	for i, k := range r.Filter {
		rules[i] = s3api.FilterRule{Name: new(k.Part.wire()), Value: new(k.Value)}
	}
	return &s3api.NotificationConfigurationFilter{Key: &s3api.S3KeyFilter{FilterRules: rules}}
}

// subject is the Subject S3 publishes to a topic with.
const subject = "Amazon S3 Notification"

// Requests are the deliveries ev raises: one per rule that selects it, in
// rule order, each carrying the record with that rule's Id. A change that
// is nil or names no event S3 reports raises none and is a *ChangeError.
func (c Config) Requests(ev Event) ([]delivery.Request, error) {
	if err := checkChange(ev.Change); err != nil {
		return nil, err
	}
	var out []delivery.Request
	for i := range c.Rules {
		r := &c.Rules[i]
		if !r.selects(ev.Change, ev.Key) {
			continue
		}
		body, err := Record(ev, r.ID)
		if err != nil {
			return nil, err
		}
		req, err := r.request(ev.Bucket.ARN, body)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// TestRequests are the s3:TestEvent deliveries a validated configuration
// sends: one to each queue and topic, none to a function.
func (c Config) TestRequests(b Bucket, at time.Time, req Request) ([]delivery.Request, error) {
	body, err := TestEvent(b.Name, at, req)
	if err != nil {
		return nil, err
	}
	var out []delivery.Request
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Kind == Function {
			continue
		}
		req, err := r.request(b.ARN, body)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// request is the delivery of body to the rule's destination.
func (r *Rule) request(source aws.ARN, body []byte) (delivery.Request, error) {
	req := delivery.Request{Source: source, Policy: delivery.S3Notification{}}
	switch r.Kind {
	case Queue:
		req.Mechanism, req.Target = delivery.S3ToSQS, delivery.SQSQueue{ARN: r.ARN, Body: string(body)}
	case Topic:
		req.Mechanism, req.Target = delivery.S3ToSNS, delivery.SNSTopic{ARN: r.ARN, Message: string(body), Subject: subject}
	case Function:
		req.Mechanism, req.Target = delivery.S3ToLambda, delivery.LambdaFunction{ARN: r.ARN, Event: body, Invocation: delivery.InvocationEvent}
	default:
		return delivery.Request{}, &KindError{Kind: r.Kind}
	}
	return req, nil
}
