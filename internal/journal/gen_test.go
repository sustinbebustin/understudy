package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

var (
	epoch = clock.FromWire(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	wall0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Small pools so random filters hit random events. EventBridge's id and
	// ARN namespace differ, as in the registry.
	poolServices   = []Service{{"sqs", "sqs"}, {"sns", "sns"}, {"lambda", "lambda"}, {"eventbridge", "events"}}
	poolOperations = []string{"SendMessage", "Publish", "Invoke"}
	poolRequestIDs = []ids.RequestID{"req-a", "req-b", "req-c", ""}
	poolPrincipals = []string{"arn:aws:iam::000000000000:root", "arn:aws:sts::000000000000:assumed-role/worker/s", ""}
	poolResources  = []string{"orders", "orders-dlq", "billing"}
)

// newTestJournal returns a journal on a frozen clock at epoch whose wall
// clock reads wall0.
func newTestJournal(tb rapid.TB, o Options) *Journal {
	tb.Helper()
	return newJournalWithWall(tb, o, wall0)
}

// newJournalWithWall returns a journal on a frozen clock at epoch whose wall
// clock reads wall.
func newJournalWithWall(tb rapid.TB, o Options, wall time.Time) *Journal {
	tb.Helper()
	return newJournalOpening(tb, o, wall, openOSFile)
}

// newJournalOpening is newJournalWithWall with open opening the file.
func newJournalOpening(tb rapid.TB, o Options, wall time.Time, open opener) *Journal {
	tb.Helper()
	c, err := clock.New(clock.Options{Mode: clock.Frozen{At: epoch}, Wall: func() time.Time { return wall }})
	if err != nil {
		tb.Fatalf("clock.New: %v", err)
	}
	o.Clock = c
	j, err := newJournal(o, open)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	return j
}

// mustAppend appends e, failing when the journal refuses it.
func mustAppend(tb rapid.TB, j *Journal, e Event) ref.EventID {
	tb.Helper()
	id, err := j.Append(e)
	if err != nil {
		tb.Fatalf("Append: %v", err)
	}
	return id
}

// mustBegin begins e, failing when the journal refuses it.
func mustBegin(tb rapid.TB, j *Journal, e Event) ref.EventID {
	tb.Helper()
	id, err := j.Begin(e)
	if err != nil {
		tb.Fatalf("Begin: %v", err)
	}
	return id
}

// mustRecord records e as the kernel would: an APICall through Begin, then
// End unless its outcome is Pending, and anything else through Append.
func mustRecord(tb rapid.TB, j *Journal, e Event) ref.EventID {
	tb.Helper()
	call, ok := e.Detail.(APICall)
	if !ok {
		return mustAppend(tb, j, e)
	}
	id := mustBegin(tb, j, e)
	if _, pending := call.Outcome.(Pending); !pending {
		if err := j.End(id, call.Outcome, call.Latency); err != nil {
			tb.Fatalf("End(%d): %v", id, err)
		}
	}
	return id
}

func scope() aws.Scope {
	acct, err := aws.ParseAccountID("000000000000")
	if err != nil {
		panic(err)
	}
	return aws.Scope{Partition: aws.PartitionAWS(), Account: acct, Region: aws.DefaultRegion()}
}

func arn(svc, name string) aws.ARN {
	return aws.ARN{Partition: "aws", Service: svc, Region: "us-east-1", Account: "000000000000", Resource: name}
}

// genARN draws ARNs from the pools, and now and then the zero ARN, which is
// no resource.
func genARN() *rapid.Generator[aws.ARN] {
	return rapid.Custom(func(t *rapid.T) aws.ARN {
		if rapid.IntRange(0, 9).Draw(t, "zero?") == 0 {
			return aws.ARN{}
		}
		return arn(rapid.SampledFrom(poolServices).Draw(t, "svc").ARNNamespace, rapid.SampledFrom(poolResources).Draw(t, "name"))
	})
}

// nilIfEmpty maps an empty slice to nil, the form decoding produces.
func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

func genInstant() *rapid.Generator[clock.Instant] {
	return rapid.Custom(func(t *rapid.T) clock.Instant {
		return clock.FromWire(time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "sec"), rapid.Int64Range(0, 999_999_999).Draw(t, "nsec")))
	})
}

func genWall() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time { return genInstant().Draw(t, "wall").Wire() })
}

// genText draws valid UTF-8, the only text decoding produces.
func genText() *rapid.Generator[string] { return rapid.String() }

// genByteText draws text that may hold bytes that are not UTF-8, as raw HTTP
// requests and error messages echoing them can.
func genByteText() *rapid.Generator[string] {
	return rapid.OneOf(rapid.String(), rapid.Custom(func(t *rapid.T) string {
		return string(rapid.SliceOf(rapid.Byte()).Draw(t, "bytes"))
	}))
}

func genOrigin(text *rapid.Generator[string]) *rapid.Generator[ref.Origin] {
	external := rapid.Custom(func(t *rapid.T) ref.Origin {
		var addr netip.AddrPort
		if rapid.Bool().Draw(t, "hasAddr") {
			b := rapid.SliceOfN(rapid.Byte(), 4, 4).Draw(t, "ip")
			addr = netip.AddrPortFrom(netip.AddrFrom4([4]byte(b)), rapid.Uint16().Draw(t, "port"))
		}
		return ref.External{
			Protocol:   rapid.SampledFrom([]ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery, ref.RESTXML}).Draw(t, "protocol"),
			Listener:   rapid.SampledFrom([]ref.ListenerKind{ref.ListenerMain, ref.ListenerContainer}).Draw(t, "listener"),
			RemoteAddr: addr,
			UserAgent:  text.Draw(t, "ua"),
		}
	})
	internal := rapid.Custom(func(t *rapid.T) ref.Origin {
		return ref.Internal{Via: rapid.SampledFrom([]ref.Via{
			ref.SNSDelivery, ref.LambdaESM, ref.EventsTarget, ref.S3Notification, ref.LambdaLogs,
			ref.DynamoDBTTL, ref.Timer, ref.Snapshot, ref.Admin, ref.KMSForward, ref.STSAssumeRole,
		}).Draw(t, "via")}
	})
	return rapid.OneOf(external, internal)
}

func genIdentity() *rapid.Generator[ErrorIdentity] {
	return rapid.Custom(func(t *rapid.T) ErrorIdentity {
		return ErrorIdentity{Shape: rapid.SampledFrom([]string{"com.amazonaws.sqs#QueueDoesNotExist", "com.amazonaws.sqs#RequestThrottled"}).Draw(t, "shape")}
	})
}

func genOutcome() *rapid.Generator[Outcome] { return genOutcomeOf(genText()) }

func genOutcomeOf(text *rapid.Generator[string]) *rapid.Generator[Outcome] {
	return rapid.OneOf(
		rapid.Just[Outcome](Pending{}),
		rapid.Just[Outcome](OK{}),
		rapid.Custom(func(t *rapid.T) Outcome {
			return Failed{Identity: genIdentity().Draw(t, "id"), Status: rapid.IntRange(0, 599).Draw(t, "status"), Message: text.Draw(t, "msg")}
		}),
		rapid.Custom(func(t *rapid.T) Outcome {
			return Injected{Rule: ref.RuleID(text.Draw(t, "rule")), Identity: genIdentity().Draw(t, "id")}
		}),
	)
}

// genEnded is an outcome End accepts.
func genEnded() *rapid.Generator[Outcome] {
	return genOutcome().Filter(func(o Outcome) bool { _, pending := o.(Pending); return !pending })
}

func genDeliveryOutcome(text *rapid.Generator[string]) *rapid.Generator[DeliveryOutcome] {
	return rapid.OneOf(
		rapid.Just[DeliveryOutcome](Delivered{}),
		rapid.Custom(func(t *rapid.T) DeliveryOutcome { return Filtered{Rule: text.Draw(t, "rule")} }),
		rapid.Custom(func(t *rapid.T) DeliveryOutcome { return Retrying{Next: genInstant().Draw(t, "next")} }),
		rapid.Custom(func(t *rapid.T) DeliveryOutcome { return DeadLettered{DLQ: genARN().Draw(t, "dlq")} }),
		rapid.Custom(func(t *rapid.T) DeliveryOutcome { return Dropped{Reason: text.Draw(t, "reason")} }),
	)
}

func genInvocationOutcome() *rapid.Generator[InvocationOutcome] {
	return rapid.OneOf(
		rapid.Just[InvocationOutcome](InvocationOK{}),
		rapid.Custom(func(t *rapid.T) InvocationOutcome {
			return FunctionError{Type: rapid.SampledFrom([]string{"Unhandled", "Handled"}).Draw(t, "type")}
		}),
		rapid.Just[InvocationOutcome](Timeout{}),
		rapid.Custom(func(t *rapid.T) InvocationOutcome { return ExitError{Code: rapid.IntRange(-1, 255).Draw(t, "code")} }),
		rapid.Just[InvocationOutcome](OOM{}),
		rapid.Just[InvocationOutcome](Throttled{}),
	)
}

func genMode() *rapid.Generator[clock.Mode] {
	return rapid.OneOf(
		rapid.Custom(func(t *rapid.T) clock.Mode {
			return clock.Running{Offset: time.Duration(rapid.Int64().Draw(t, "offset"))}
		}),
		rapid.Custom(func(t *rapid.T) clock.Mode { return clock.Frozen{At: genInstant().Draw(t, "at")} }),
		rapid.Custom(func(t *rapid.T) clock.Mode {
			return clock.Scaled{
				Factor:     rapid.Float64Range(0.001, clock.MaxScale).Draw(t, "factor"),
				AnchorWall: genWall().Draw(t, "anchorWall"),
				AnchorEmu:  genInstant().Draw(t, "anchorEmu"),
			}
		}),
	)
}

// genRedacted draws no input or a JSON object of arbitrary text, written
// without HTML escaping so '<', '>', '&', U+2028 and U+2029 appear raw.
func genRedacted() *rapid.Generator[Redacted] {
	return rapid.Custom(func(t *rapid.T) Redacted {
		var doc bytes.Buffer
		if rapid.Bool().Draw(t, "hasInput") {
			enc := json.NewEncoder(&doc)
			enc.SetEscapeHTML(false)
			members := rapid.MapOfN(rapid.String(), rapid.String(), 0, 3).Draw(t, "members")
			if err := enc.Encode(members); err != nil {
				t.Fatalf("encoding %q: %v", members, err)
			}
		}
		r, err := NewRedacted(doc.Bytes())
		if err != nil {
			t.Fatalf("NewRedacted(%s): %v", doc.Bytes(), err)
		}
		return r
	})
}

func genAPICall(text *rapid.Generator[string], outcome *rapid.Generator[Outcome]) *rapid.Generator[APICall] {
	return rapid.Custom(func(t *rapid.T) APICall {
		return APICall{
			Op: ref.OperationID{
				Service: rapid.SampledFrom(poolServices).Draw(t, "service").ID,
				Name:    rapid.SampledFrom(poolOperations).Draw(t, "operation"),
			},
			RequestID:   rapid.SampledFrom(poolRequestIDs).Draw(t, "requestId"),
			Origin:      genOrigin(text).Draw(t, "origin"),
			Principal:   rapid.SampledFrom(poolPrincipals).Draw(t, "principal"),
			Mode:        rapid.SampledFrom([]string{"", "caller", "execution-role", "service:sns.amazonaws.com"}).Draw(t, "mode"),
			Resources:   nilIfEmpty(rapid.SliceOfNDistinct(genARN(), 0, 3, aws.ARN.String).Draw(t, "resources")),
			Outcome:     outcome.Draw(t, "outcome"),
			Latency:     time.Duration(rapid.Int64Range(0, int64(time.Minute)).Draw(t, "latency")),
			TraceHeader: rapid.OneOf(rapid.Just(""), text).Draw(t, "traceHeader"),
			Input:       genRedacted().Draw(t, "input"),
		}
	})
}

// genDetail draws any Detail variant, as decoding would produce it.
func genDetail() *rapid.Generator[Detail] { return genDetailOf(genText()) }

// genDetailOf draws any Detail variant with free text drawn from text.
func genDetailOf(text *rapid.Generator[string]) *rapid.Generator[Detail] {
	return rapid.OneOf(
		rapid.Custom(func(t *rapid.T) Detail { return genAPICall(text, genOutcomeOf(text)).Draw(t, "call") }),
		rapid.Custom(func(t *rapid.T) Detail {
			return Delivery{
				From: genARN().Draw(t, "from"),
				To:   genARN().Draw(t, "to"),
				Mechanism: rapid.SampledFrom([]delivery.Mechanism{
					delivery.SNSToSQS, delivery.SNSToLambda, delivery.S3ToSQS, delivery.S3ToSNS,
					delivery.S3ToLambda, delivery.EventsToLambda, delivery.EventsToSQS, delivery.EventsToSNS,
				}).Draw(t, "mechanism"),
				Attempt: rapid.IntRange(1, 100).Draw(t, "attempt"),
				Outcome: genDeliveryOutcome(text).Draw(t, "outcome"),
			}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return Invocation{
				Function: genARN().Draw(t, "function"),
				Version:  rapid.SampledFrom([]string{"$LATEST", "3"}).Draw(t, "version"),
				Env:      text.Draw(t, "env"),
				Cold:     rapid.Bool().Draw(t, "cold"),
				Init:     time.Duration(rapid.Int64Range(0, int64(time.Second)).Draw(t, "init")),
				Duration: time.Duration(rapid.Int64Range(0, int64(time.Minute)).Draw(t, "duration")),
				Outcome:  genInvocationOutcome().Draw(t, "outcome"),
			}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return TimerFired{Key: clock.TimerKey{Kind: clock.TimerKind(text.Draw(t, "kind")), ID: text.Draw(t, "id")}}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			states := []ResourceState{"Pending", "Active", "Failed"}
			return StateChange{Resource: genARN().Draw(t, "resource"), From: rapid.SampledFrom(states).Draw(t, "from"), To: rapid.SampledFrom(states).Draw(t, "to")}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return FaultInjected{Rule: ref.RuleID(text.Draw(t, "rule")), Hit: rapid.IntRange(1, 9).Draw(t, "hit"), Of: rapid.IntRange(0, 9).Draw(t, "of")}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return ClockChanged{From: genMode().Draw(t, "from"), To: genMode().Draw(t, "to")}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return Advisory{
				Kind:     rapid.SampledFrom([]AdvisoryKind{AdvisoryPolicyWouldDeny, AdvisoryUnmintedCredentials, AdvisoryShadowedCodeUpdate, AdvisoryCrossRegionServed}).Draw(t, "kind"),
				Resource: genARN().Draw(t, "resource"),
				Text:     text.Draw(t, "text"),
			}
		}),
		rapid.Custom(func(t *rapid.T) Detail {
			return Unclaimed{
				Method:  rapid.SampledFrom([]string{"GET", "POST"}).Draw(t, "method"),
				Host:    text.Draw(t, "host"),
				Path:    text.Draw(t, "path"),
				Reasons: rapid.SliceOfN(text, 0, 3).Draw(t, "reasons"),
			}
		}),
		rapid.Custom(func(t *rapid.T) Detail { return Gap{Missed: rapid.Uint64().Draw(t, "missed")} }),
	)
}

// genAppendable draws a Detail Append accepts: any variant but APICall and
// Gap.
func genAppendable() *rapid.Generator[Detail] {
	return genDetail().Filter(func(d Detail) bool { k := KindOf(d); return k != KindAPICall && k != KindGap })
}

// genEvent draws a complete event, as a decoder would produce it.
func genEvent() *rapid.Generator[Event] {
	return rapid.Custom(func(t *rapid.T) Event {
		detail := genDetail().Draw(t, "detail")
		if u, ok := detail.(Unclaimed); ok {
			u.Reasons = nilIfEmpty(u.Reasons)
			detail = u
		}
		return Event{
			ID:     ref.EventID(rapid.Uint64().Draw(t, "id")),
			Cause:  ref.Cause{Parent: ref.EventID(rapid.Uint64().Draw(t, "parent")), Trace: ref.EventID(rapid.Uint64().Draw(t, "trace"))},
			Links:  nilIfEmpty(rapid.SliceOfN(rapid.Custom(func(t *rapid.T) ref.EventID { return ref.EventID(rapid.Uint64().Draw(t, "link")) }), 0, 3).Draw(t, "links")),
			At:     genInstant().Draw(t, "at"),
			Wall:   genWall().Draw(t, "wall"),
			Scope:  scope(),
			Detail: detail,
		}
	})
}

// detailName labels a detail in failure messages.
func detailName(d Detail) string { return fmt.Sprintf("%s %+v", KindOf(d), d) }
