package fault

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Members is an error action's members: a JSON object of the error shape's
// members in the awsJson1_0 form, names as modeled and never jsonName,
// whatever the service's protocol. Add decodes them strictly through the
// shape's generated deserializer, which it reaches through the Catalogue.
// Their order is part of the value: the rule's JSON form keeps it.
type Members document.Map

// JSONVerbatim marks Members as a jsonx.Verbatim value, so a persisted rule
// keeps its members in the order given.
func (Members) JSONVerbatim() {}

// MarshalJSON writes the members as a compact JSON object in the order
// given.
func (m Members) MarshalJSON() ([]byte, error) {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(document.Map(m))
	return e.Bytes()
}

// UnmarshalJSON reads a JSON object of members, refusing any other JSON
// value and a duplicate member name at any depth; null reads as no
// members.
func (m *Members) UnmarshalJSON(data []byte) error {
	d := jsonx.NewDecoder(data)
	if k, err := d.Peek(); err == nil && k == jsonx.Null {
		if err := d.ReadNull(); err != nil {
			return err
		}
		*m = nil
		return d.End()
	}
	v, err := readMembers(d, "")
	if err != nil {
		return err
	}
	if err := d.End(); err != nil {
		return err
	}
	*m = Members(v)
	return nil
}

// readMembers reads the object of members at path at below the members.
func readMembers(d *jsonx.Decoder, at string) (document.Map, error) {
	if err := d.BeginObject(); err != nil {
		return nil, err
	}
	out := document.Map{}
	for {
		name, ok, err := d.NextName()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		if slices.ContainsFunc(out, func(e document.Entry) bool { return e.Key == name }) {
			if at == "" {
				return nil, fmt.Errorf("fault: duplicate member %q", name)
			}
			return nil, fmt.Errorf("fault: duplicate member %q in %s", name, at)
		}
		v, err := readStrict(d, join(at, name))
		if err != nil {
			return nil, err
		}
		out = append(out, document.Entry{Key: name, Value: v})
	}
}

// readStrict reads one value as jsonx.Decoder.ReadValue does, refusing a
// duplicate name in any object inside it.
func readStrict(d *jsonx.Decoder, at string) (document.Value, error) {
	k, err := d.Peek()
	if err != nil {
		return nil, err
	}
	switch k {
	case jsonx.Object:
		return readMembers(d, at)
	case jsonx.Array:
		if err := d.BeginArray(); err != nil {
			return nil, err
		}
		l := document.List{}
		for {
			more, err := d.NextElement()
			if err != nil {
				return nil, err
			}
			if !more {
				return l, nil
			}
			v, err := readStrict(d, join(at, strconv.Itoa(len(l))))
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
	case jsonx.Null, jsonx.Bool, jsonx.String, jsonx.Number:
		return d.ReadValue()
	}
	return d.ReadValue()
}

func join(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

// clone returns a deep copy of m, nil for nil.
func (m Members) clone() Members {
	if m == nil {
		return nil
	}
	v, ok := cloneValue(document.Map(m)).(document.Map)
	if !ok {
		return nil
	}
	return Members(v)
}

func cloneValue(v document.Value) document.Value {
	switch v := v.(type) {
	case document.Map:
		out := make(document.Map, len(v))
		for i, e := range v {
			out[i] = document.Entry{Key: e.Key, Value: cloneValue(e.Value)}
		}
		return out
	case document.List:
		out := make(document.List, len(v))
		for i, x := range v {
			out[i] = cloneValue(x)
		}
		return out
	case document.Null, document.Bool, document.Number, document.String, nil:
		return v
	}
	return v
}

// member returns the value of the member named name.
func (m Members) member(name string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == name {
			return e.Value, true
		}
	}
	return nil, false
}

// with returns m with member name set to v, in its place when m has it,
// else last; m itself is unchanged.
func (m Members) with(name string, v document.Value) Members {
	out := slices.Clone(m)
	for i := range out {
		if out[i].Key == name {
			out[i].Value = v
			return out
		}
	}
	return append(out, document.Entry{Key: name, Value: v})
}

// without returns m without member name; m itself is unchanged.
func (m Members) without(name string) Members {
	return slices.DeleteFunc(slices.Clone(m), func(e document.Entry) bool { return e.Key == name })
}

// cloneAction returns a with its own copy of any members, so a rule the
// store holds cannot change underneath it.
func cloneAction(a Action) Action {
	if e, ok := a.(Error); ok {
		e.Members = e.Members.clone()
		return e
	}
	return a
}

// The DynamoDB shapes whose members the store checks beyond their model,
// so an injected error is shaped as AWS sends it (smithy-protocol-tests.md
// 12.1, 14.9).
const (
	transactionCanceled smithy.ShapeID = "com.amazonaws.dynamodb#TransactionCanceledException"
	conditionalCheck    smithy.ShapeID = "com.amazonaws.dynamodb#ConditionalCheckFailedException"
	reasonsMember                      = "CancellationReasons"
	// maxReasons is the @length max of CancellationReasonList, which
	// matches the 100 items TransactItems holds (smithy-protocol-tests.md
	// 12.1, 12.2).
	maxReasons = 100
)

// memberPlan is what a hit of a validated error action with members needs
// on one operation: the name of the shape's message member, empty when the
// shape has none.
type memberPlan struct {
	message string
}

// checkMembers validates error action a's members for op, which sends id,
// and returns what a hit needs. The checks run in order: empty members,
// a message given twice, a shape without a generated decoder or with one
// of another identity, the strict decode, then the checks of
// checkAWSShape. A
// TransactionCanceledException rule without CancellationReasons is refused
// even when it sets no members.
func checkMembers(cat Catalogue, r Rule, a Error, op ref.OperationID, id awserr.Identity) (memberPlan, *RuleError) {
	if a.Members == nil {
		if id.Shape == transactionCanceled {
			return memberPlan{}, reject(r, membersField, "%s", reasonsMissing())
		}
		return memberPlan{}, nil
	}
	if len(a.Members) == 0 {
		return memberPlan{}, reject(r, membersField, "is empty; leave it out, or set members of %s", id.Shape)
	}
	if a.Message != "" {
		for _, key := range []string{"Message", "message"} {
			if _, ok := a.Members.member(key); ok {
				return memberPlan{}, reject(r, membersField+"."+key, "repeats action.message; give the message in one of them")
			}
		}
	}
	v, ok := cat.NewMembers(op, id.Shape)
	if !ok {
		return memberPlan{}, reject(r, membersField, "%s has no generated decoder, so a fault rule cannot set its members; leave action.members out", id.Shape)
	}
	if got := v.ErrorIdentity(); got != id {
		return memberPlan{}, reject(r, membersField, "the generated decoder of %s builds %s (__type %s) where %s sends %s (__type %s); "+
			"the catalogue and the generated code disagree, which is an understudy bug, not a fault of the rule, so report it",
			id.Shape, got.Shape, got.JSONType, op, id.Shape, id.JSONType)
	}
	schema, err := decodeMembers(v, a.Members)
	if err != nil {
		if me, ok := errors.AsType[*memberError](err); ok {
			return memberPlan{}, reject(r, me.At, "%s", me.Reason)
		}
		return memberPlan{}, reject(r, membersField, "could not be read into %s: %v", id.Shape, err)
	}
	if rerr := checkAWSShape(r, id, a.Members); rerr != nil {
		return memberPlan{}, rerr
	}
	var plan memberPlan
	for _, m := range schema.Members {
		if m.Name == "Message" || m.Name == "message" {
			plan.message = m.Name
		}
	}
	return plan, nil
}

// reasonsMissing is the refusal of a TransactionCanceledException rule
// without CancellationReasons (smithy-protocol-tests.md 12.2).
func reasonsMissing() string {
	return string(transactionCanceled) + ` always carries CancellationReasons on AWS, one per item of the transaction in item order; ` +
		`set them in action.members, such as {"CancellationReasons": [{"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}`
}

// checkAWSShape refuses members that decode but would make an error AWS
// never sends (smithy-protocol-tests.md 14.14): cancellation reasons
// outside 1 to 100, a reason without a code, reasons naming no failing
// item, and a ConditionalCheckFailedException carrying Item.
func checkAWSShape(r Rule, id awserr.Identity, m Members) *RuleError {
	switch id.Shape {
	case transactionCanceled:
		v, ok := m.member(reasonsMember)
		reasons, isList := v.(document.List)
		if !ok || !isList {
			return reject(r, membersField, "%s", reasonsMissing())
		}
		field := membersField + "." + reasonsMember
		switch {
		case len(reasons) == 0:
			return reject(r, field, "has 0 entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to %d; give at least one reason", maxReasons)
		case len(reasons) > maxReasons:
			return reject(r, field, "has %d entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to %d; give at most %d reasons, one per item of the transaction",
				len(reasons), maxReasons, maxReasons)
		}
		codes := reasonCodes(reasons)
		for i, c := range codes {
			if c == "" {
				return reject(r, field+"."+strconv.Itoa(i), "has no Code; every cancellation reason carries one, None for an item that did not fail")
			}
		}
		if !slices.ContainsFunc(codes, func(c string) bool { return c != noneCode }) {
			return reject(r, field, "names no failing item, since every Code is None; AWS cancels a transaction only for an item that fails, "+
				"so give one reason a Code such as TransactionConflict or ConditionalCheckFailed")
		}
	case conditionalCheck:
		if _, ok := m.member("Item"); ok {
			// AWS spells the message key Message when Item is present
			// (testdata/parity/dynamodb/R04/05-put-ccf-all-old.json), and the
			// identity an injected error carries spells it message.
			return reject(r, membersField+".Item", "%s carries Item only under ReturnValuesOnConditionCheckFailure ALL_OLD, "+
				"when AWS also spells its message key Message (testdata/parity/dynamodb/R04/05-put-ccf-all-old.json), which an injected error does not do; leave Item out",
				conditionalCheck)
		}
	}
	return nil
}

// noneCode is the code of an item that did not fail: the literal string
// "None", not a null (smithy-protocol-tests.md 12.2, aws-doc).
const noneCode = "None"

// reasonCodes returns each reason's Code, empty for a reason without one.
func reasonCodes(reasons document.List) []string {
	codes := make([]string, len(reasons))
	for i, x := range reasons {
		if reason, ok := x.(document.Map); ok {
			if c, ok := Members(reason).member("Code"); ok {
				if s, ok := c.(document.String); ok {
					codes[i] = string(s)
				}
			}
		}
	}
	return codes
}

// reasonCount is the number of cancellation reasons a validated
// TransactionCanceledException rule gives, the fewest items a call it
// matches has (smithy-protocol-tests.md 14.9); 0 for any other rule.
func reasonCount(a Action) int {
	e, ok := a.(Error)
	if !ok {
		return 0
	}
	v, ok := e.Members.member(reasonsMember)
	if !ok {
		return 0
	}
	reasons, ok := v.(document.List)
	if !ok {
		return 0
	}
	return len(reasons)
}

// injectMembers builds the error a hit of a, an error action with members,
// injects on op, a call of items transaction items. A
// TransactionCanceledException gets one reason per item: the rule's
// reasons, then {"Code":"None"} for each item after them, with no Message
// and no Item (smithy-protocol-tests.md 12.2, 14.9; the bytes of a None
// reason are UNVERIFIED, PROTO-02). ExecuteTransaction pads its statements
// the same way, though its reasons are UNVERIFIED (12.4, PROTO-05). The
// message, first match wins: the members' own message member,
// action.message, for a TransactionCanceledException AWS's text listing
// the codes (12.4), the text naming the rule, or none for a shape without
// a message key. The value's message member carries the same text, and
// the codec writes it under id's MessageKey.
func injectMembers(cat Catalogue, op ref.OperationID, id awserr.Identity, rule ref.RuleID, a Error, plan memberPlan, items int) *awserr.Error {
	m := a.Members.clone()
	var codes []string
	if id.Shape == transactionCanceled {
		v, _ := m.member(reasonsMember)
		reasons, ok := v.(document.List)
		if !ok {
			return awserr.Internalf(&InjectionError{Rule: rule, Op: op, Shape: id.Shape, Reason: "its members hold no list of CancellationReasons"})
		}
		for len(reasons) < items {
			reasons = append(reasons, document.Map{{Key: "Code", Value: document.String(noneCode)}})
		}
		m = m.with(reasonsMember, reasons)
		codes = reasonCodes(reasons)
	}
	msg := injectedMessage(m, plan, a.Message, id, rule, codes)
	if plan.message != "" {
		m = m.without(plan.message)
		if msg != "" {
			m = m.with(plan.message, document.String(msg))
		}
	}
	v, ok := cat.NewMembers(op, id.Shape)
	if !ok {
		return awserr.Internalf(&InjectionError{Rule: rule, Op: op, Shape: id.Shape, Reason: "its service no longer has a decoder for the shape"})
	}
	if _, err := decodeMembers(v, m); err != nil {
		return awserr.Internalf(&InjectionError{Rule: rule, Op: op, Shape: id.Shape, Reason: "its members no longer decode: " + err.Error()})
	}
	return awserr.WithMembers(v, msg, awserr.Injected{Rule: rule})
}

// injectedMessage picks the message of an injected error with members.
func injectedMessage(m Members, plan memberPlan, message string, id awserr.Identity, rule ref.RuleID, codes []string) string {
	if plan.message != "" {
		if v, ok := m.member(plan.message); ok {
			if s, ok := v.(document.String); ok && s != "" {
				return string(s)
			}
		}
	}
	switch {
	case message != "":
		return message
	case id.Shape == transactionCanceled:
		// The codes in request order inside AWS's frame, recorded for
		// TransactWriteItems only (smithy-protocol-tests.md 12.3,
		// third-party-recorded; 12.4). ExecuteTransaction takes the same
		// frame, which is UNVERIFIED (12.4, PROTO-05). The key it goes
		// under is the identity's MessageKey, Message from the model's
		// spelling: inferred, UNVERIFIED (12.4, 14.9, PROTO-01).
		return "Transaction cancelled, please refer cancellation reasons for specific reasons [" + strings.Join(codes, ", ") + "]"
	case id.MessageKey != awserr.NoMessage:
		return "Injected by understudy fault rule " + string(rule) + "."
	}
	return ""
}

// InjectionError is a validated rule whose members could not be turned
// into the error at a hit, which validation makes impossible; the call
// answers InternalFailure and the rule stays active.
type InjectionError struct {
	Rule  ref.RuleID
	Op    ref.OperationID
	Shape smithy.ShapeID
	// Reason says what failed.
	Reason string
}

func (e *InjectionError) Error() string {
	return fmt.Sprintf("fault rule %q matched %s but could not build %s: %s; the call answered InternalFailure and the rule is unchanged, so remove it and add it again to validate it anew",
		e.Rule, e.Op, e.Shape, e.Reason)
}
