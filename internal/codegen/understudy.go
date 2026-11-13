package codegen

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// understudyNamespace holds the overlay traits of ARCHITECTURE.md 5.2. The
// set is closed: any other id in the namespace fails generation.
const understudyNamespace = "dev.understudy#"

// The closed set of dev.understudy traits.
const (
	utEvidence            = understudyNamespace + "evidence"
	utErrorNamespace      = understudyNamespace + "errorNamespace"
	utJSONType            = understudyNamespace + "jsonType"
	utMessageByProtocol   = understudyNamespace + "messageByProtocol"
	utMessageKey          = understudyNamespace + "messageKey"
	utValidationError     = understudyNamespace + "validationError"
	utValidationPathStyle = understudyNamespace + "validationPathStyle"
	utSuccessCode         = understudyNamespace + "successCode"
	utQueryKeyAlias       = understudyNamespace + "queryKeyAlias"
	utEmptyCollection     = understudyNamespace + "emptyCollection"
	utInternalOperation   = understudyNamespace + "internalOperation"
	utPatternEcho         = understudyNamespace + "patternEcho"
	utPatternValidator    = understudyNamespace + "patternValidator"
	utAlwaysEmit          = understudyNamespace + "alwaysEmit"
	utOptionalOutput      = understudyNamespace + "optionalOutput"
	utMaxRequestBytes     = understudyNamespace + "maxRequestBytes"
	utThrottleError       = understudyNamespace + "throttleError"
	utDenialError         = understudyNamespace + "denialError"
	utNotConfigured       = understudyNamespace + "notConfigured"
	utMalformedRequest    = understudyNamespace + "malformedRequestError"
	utStatusOverride      = understudyNamespace + "statusOverride"
	utUnknownOperation    = understudyNamespace + "unknownOperationError"
	utUnknownAnswer       = understudyNamespace + "unknownOperationAnswer"
	utQueryErrorEnvelope  = understudyNamespace + "queryErrorEnvelope"
	utQueryErrorHeader    = understudyNamespace + "queryErrorHeader"
	utResponseCRC32       = understudyNamespace + "responseCrc32"
	utXMLDeclaration      = understudyNamespace + "xmlDeclaration"
	utRefuseDuplicates    = understudyNamespace + "refuseDuplicateMembers"
	utEnumOrder           = understudyNamespace + "enumOrder"
	utValidationInputRoot = understudyNamespace + "validationInputRoot"
)

// site is where a trait sits once overlays are merged.
type site uint8

const (
	siteService site = 1 << iota
	siteOperation
	siteMember
	siteErrorShape
	siteStringShape
	siteAnyShape
	siteEnumShape
)

func (s site) String() string {
	names := map[site]string{
		siteService: "a service shape", siteOperation: "an operation shape", siteMember: "a member",
		siteErrorShape: "an error structure", siteStringShape: "a string shape", siteAnyShape: "any shape",
		siteEnumShape: "an enum shape",
	}
	var out []string
	for _, b := range []site{siteService, siteOperation, siteMember, siteErrorShape, siteStringShape, siteAnyShape, siteEnumShape} {
		if s&b != 0 {
			out = append(out, names[b])
		}
	}
	return strings.Join(out, " or ")
}

// understudyTrait says what a trait does, where it may sit and how its
// value parses.
type understudyTrait struct {
	class Class
	sites site
	parse func(json.RawMessage) error
}

// understudyTraits is the closed set with each trait's class: evidence only
// labels an overlay entry, and every other trait changes the wire, the
// validation or the operations the generator emits (ARCHITECTURE.md 5.2).
func understudyTraits() map[string]understudyTrait {
	shapeRef := func(raw json.RawMessage) error { _, err := parseShapeRef(raw); return err }
	return map[string]understudyTrait{
		utEvidence:            {GenerationOnly, siteAnyShape | siteMember, func(r json.RawMessage) error { _, err := parseEvidence(r); return err }},
		utErrorNamespace:      {Honor, siteService, func(r json.RawMessage) error { _, err := parseErrorNamespace(r); return err }},
		utJSONType:            {Honor, siteErrorShape, func(r json.RawMessage) error { _, err := parseJSONType(r); return err }},
		utMessageByProtocol:   {Honor, siteErrorShape, func(r json.RawMessage) error { _, err := parseMessageByProtocol(r); return err }},
		utMessageKey:          {Honor, siteErrorShape, func(r json.RawMessage) error { _, err := parseMessageKey(r); return err }},
		utValidationError:     {Honor, siteService | siteOperation | siteMember, func(r json.RawMessage) error { _, err := parseValidationRules(r); return err }},
		utValidationPathStyle: {Honor, siteService, func(r json.RawMessage) error { _, err := parsePathStyle(r); return err }},
		utSuccessCode:         {Honor, siteOperation, func(r json.RawMessage) error { _, err := parseStatus(r); return err }},
		utQueryKeyAlias:       {Honor, siteMember, func(r json.RawMessage) error { _, err := parseQueryKeyAlias(r); return err }},
		utEmptyCollection:     {Honor, siteService | siteMember, func(r json.RawMessage) error { _, err := parseEmptyCollection(r); return err }},
		utInternalOperation:   {Honor, siteOperation, parseAnnotation},
		utPatternEcho:         {Honor, siteStringShape, func(r json.RawMessage) error { _, err := parseNonEmptyString(r); return err }},
		utPatternValidator:    {Honor, siteStringShape, func(r json.RawMessage) error { _, err := parsePatternValidator(r); return err }},
		utAlwaysEmit:          {Honor, siteMember, parseAnnotation},
		utOptionalOutput:      {Honor, siteMember, parseAnnotation},
		utMaxRequestBytes:     {Honor, siteOperation, func(r json.RawMessage) error { _, err := parseMaxRequestBytes(r); return err }},
		utThrottleError:       {Honor, siteService, shapeRef},
		utDenialError:         {Honor, siteService, shapeRef},
		utNotConfigured:       {Honor, siteOperation, func(r json.RawMessage) error { _, err := parseNotConfigured(r); return err }},
		utMalformedRequest:    {Honor, siteService, shapeRef},
		utStatusOverride:      {Honor, siteErrorShape, func(r json.RawMessage) error { _, err := parseStatus(r); return err }},
		utUnknownOperation:    {Honor, siteService, func(r json.RawMessage) error { _, err := parseUnknownOperation(r); return err }},
		utUnknownAnswer:       {Honor, siteService, func(r json.RawMessage) error { _, err := parseUnknownOperationAnswer(r); return err }},
		utQueryErrorEnvelope:  {Honor, siteService, func(r json.RawMessage) error { _, err := parseQueryErrorEnvelope(r); return err }},
		utQueryErrorHeader:    {Honor, siteErrorShape, func(r json.RawMessage) error { _, err := parseQueryErrorHeader(r); return err }},
		utResponseCRC32:       {Honor, siteService, parseAnnotation},
		utXMLDeclaration:      {Honor, siteService, parseAnnotation},
		utRefuseDuplicates:    {Honor, siteService, parseAnnotation},
		utEnumOrder:           {Honor, siteEnumShape, func(r json.RawMessage) error { _, err := parseEnumOrder(r); return err }},
		utValidationInputRoot: {Honor, siteService, parseAnnotation},
	}
}

// parseEnumOrder reads dev.understudy#enumOrder: the enum's values in the
// order AWS lists them in an enum violation, when it is not the model's.
// The shape's IR checks that it is a permutation of the values.
func parseEnumOrder(raw json.RawMessage) ([]string, error) {
	var v []string
	if err := decodeTrait(raw, &v); err != nil {
		return nil, errors.New("enumOrder takes the enum's values as a list of strings")
	}
	if len(v) == 0 {
		return nil, errors.New("enumOrder is empty; the model's order needs no trait")
	}
	return v, nil
}

// parseQueryErrorEnvelope reads {namespace?, detail?}. A value with neither
// set says what the absence of the trait already says, so it is refused.
func parseQueryErrorEnvelope(raw json.RawMessage) (registry.QueryErrorEnvelope, error) {
	var v struct {
		Namespace bool `json:"namespace"`
		Detail    bool `json:"detail"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return registry.QueryErrorEnvelope{}, err
	}
	if !v.Namespace && !v.Detail {
		return registry.QueryErrorEnvelope{}, errors.New(`set "namespace" or "detail" to true; an envelope that follows Smithy's example needs no trait`)
	}
	return registry.QueryErrorEnvelope{Namespace: v.Namespace, Detail: v.Detail}, nil
}

// parseUnknownOperationAnswer reads dev.understudy#unknownOperationAnswer:
// "awsFault" or "notFound". The identity in the protocol's envelope is the
// absence of the trait, so it has no spelling.
func parseUnknownOperationAnswer(raw json.RawMessage) (registry.UnknownOperationAnswer, error) {
	var v string
	if err := decodeTrait(raw, &v); err != nil {
		return 0, errors.New(`unknownOperationAnswer takes a string: "awsFault" or "notFound"`)
	}
	switch v {
	case "awsFault":
		return registry.UnknownOperationAWSFault, nil
	case "notFound":
		return registry.UnknownOperationNotFound, nil
	}
	return 0, fmt.Errorf(`unknownOperationAnswer is %q; the values are "awsFault" and "notFound"`, v)
}

// parseQueryErrorHeader reads dev.understudy#queryErrorHeader, {send}: when
// a query-compatible service's JSON or CBOR answer sends the error's
// x-amzn-query-error. "query-mode" sends it only when the request sent
// x-amzn-query-mode: true. Sending it on every error is the absence of the
// trait, so "always" is refused.
func parseQueryErrorHeader(raw json.RawMessage) (awserr.QueryErrorWhen, error) {
	var v struct {
		Send string `json:"send"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return 0, err
	}
	switch v.Send {
	case "query-mode":
		return awserr.QueryErrorInQueryMode, nil
	case "always":
		return 0, errors.New(`"send": "always" is what the absence of the trait already says; drop the trait`)
	}
	return 0, fmt.Errorf(`"send" is %q; the one value is "query-mode"`, v.Send)
}

// Evidence is one item of dev.understudy#evidence: what backs an overlay
// entry, or, in the list form, the traits of the entry it Covers.
type Evidence struct {
	Label     evidence.Label
	Source    string
	Validated string   // YYYY-MM-DD, optional
	Covers    []string // trait ids; empty in the single-object form, which covers the whole entry
}

// parseEvidence accepts one {label, source, validated?} object for the
// whole entry, or a list of them each naming the traits it covers, for an
// entry whose traits rest on different evidence.
func parseEvidence(raw json.RawMessage) ([]Evidence, error) {
	items, list, err := evidenceItems(raw)
	if err != nil {
		return nil, err
	}
	out := make([]Evidence, 0, len(items))
	for i, v := range items {
		label, err := evidence.ParseLabel(v.Label)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		if v.Source == "" {
			return nil, fmt.Errorf(`item %d: "source" is required: a URL or a docs/research reference that backs the entry`, i)
		}
		if v.Validated != "" {
			if _, err := time.Parse(time.DateOnly, v.Validated); err != nil {
				return nil, fmt.Errorf(`item %d: "validated" is %q; use YYYY-MM-DD`, i, v.Validated)
			}
		}
		if list && len(v.Covers) == 0 {
			return nil, fmt.Errorf(`item %d: the list form names the traits each item covers in "covers"`, i)
		}
		out = append(out, Evidence{Label: label, Source: v.Source, Validated: v.Validated, Covers: v.Covers})
	}
	return out, nil
}

// evidenceItem is one evidence object as written.
type evidenceItem struct {
	Label     string   `json:"label"`
	Source    string   `json:"source"`
	Validated string   `json:"validated"`
	Covers    []string `json:"covers"`
}

// evidenceItems decodes the single-object or the list form; list reports
// which.
func evidenceItems(raw json.RawMessage) (items []evidenceItem, list bool, err error) {
	if len(raw) > 0 && raw[0] == '[' {
		if err := decodeTrait(raw, &items); err != nil {
			return nil, true, err
		}
		if len(items) == 0 {
			return nil, true, errors.New("the evidence list is empty")
		}
		return items, true, nil
	}
	var one evidenceItem
	if err := decodeTrait(raw, &one); err != nil {
		return nil, false, err
	}
	if len(one.Covers) > 0 {
		return nil, false, errors.New(`"covers" belongs to the list form`)
	}
	return []evidenceItem{one}, false, nil
}

// covering returns the items of ev that back trait: the single-object
// form, which covers the whole entry, or the list items naming it.
func covering(ev []Evidence, trait string) []Evidence {
	var out []Evidence
	for _, e := range ev {
		if len(e.Covers) == 0 || slices.Contains(e.Covers, trait) {
			out = append(out, e)
		}
	}
	return out
}

// checkCoverage requires the list form of evidence to cover every other
// trait of its entry exactly once.
func checkCoverage(ev []Evidence, traits Traits) error {
	if len(ev) == 1 && len(ev[0].Covers) == 0 {
		return nil
	}
	covered := map[string]bool{}
	for _, e := range ev {
		for _, id := range e.Covers {
			if !traits.Has(id) || id == utEvidence {
				return fmt.Errorf("evidence covers %s, which the entry does not apply", id)
			}
			if covered[id] {
				return fmt.Errorf("evidence covers %s twice", id)
			}
			covered[id] = true
		}
	}
	for _, id := range traits.IDs() {
		if id != utEvidence && !covered[id] {
			return fmt.Errorf("no evidence item covers %s", id)
		}
	}
	return nil
}

func parseAnnotation(raw json.RawMessage) error {
	var v struct{}
	if err := decodeTrait(raw, &v); err != nil {
		return errors.New("an annotation trait takes {}")
	}
	return nil
}

// jsonType is dev.understudy#jsonType: the error's __type name, and the
// namespace an awsJson1_0 __type carries when the value is an absolute
// namespace#Name (SQS's com.amazon.coral.service#InvalidParameterValueException,
// testdata/parity/sqs/R11/02-receive-max-0.json). An empty Namespace keeps
// the service's or the shape's.
type jsonType struct {
	Namespace string
	Name      string
}

// parseJSONType reads dev.understudy#jsonType: a bare name, or an absolute
// shape ID when the namespace differs too.
func parseJSONType(raw json.RawMessage) (jsonType, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return jsonType{}, err
	}
	if !strings.Contains(s, "#") {
		return jsonType{Name: s}, nil
	}
	id, err := smithy.Parse(s)
	if err != nil {
		return jsonType{}, fmt.Errorf("%w; or give a bare name such as InvalidParameterValueException", err)
	}
	return jsonType{Namespace: id.Namespace(), Name: id.Name()}, nil
}

func parseNonEmptyString(raw json.RawMessage) (string, error) {
	var s string
	if err := decodeTrait(raw, &s); err != nil {
		return "", err
	}
	if s == "" {
		return "", errors.New("the value must be a non-empty string")
	}
	return s, nil
}

// parseMessageKey reads dev.understudy#messageKey: the body key AWS sends
// an error's message under, "message" or "Message", when it is not the
// spelling of the shape's own message member or the shape has none.
func parseMessageKey(raw json.RawMessage) (awserr.MessageKey, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return 0, err
	}
	switch s {
	case "message":
		return awserr.MessageLower, nil
	case "Message":
		return awserr.MessageUpper, nil
	}
	return 0, fmt.Errorf("messageKey is %q or %q, not %q", "message", "Message", s)
}

func parseErrorNamespace(raw json.RawMessage) (string, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return "", err
	}
	if _, err := smithy.Parse(s + "#X"); err != nil {
		return "", fmt.Errorf("%q is not a Smithy namespace", s)
	}
	return s, nil
}

func parseShapeRef(raw json.RawMessage) (smithy.ShapeID, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return "", err
	}
	return smithy.Parse(s)
}

func parseStatus(raw json.RawMessage) (int, error) {
	var n int
	if err := decodeTrait(raw, &n); err != nil {
		return 0, err
	}
	if n < 100 || n > 599 {
		return 0, fmt.Errorf("status %d is not an HTTP status code", n)
	}
	return n, nil
}

func parseMaxRequestBytes(raw json.RawMessage) (int64, error) {
	var n int64
	if err := decodeTrait(raw, &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("a body cap must be positive, got %d", n)
	}
	return n, nil
}

func parseMessageByProtocol(raw json.RawMessage) (map[ref.ProtocolID]string, error) {
	var v map[string]string
	if err := decodeTrait(raw, &v); err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, errors.New("name at least one protocol")
	}
	out := make(map[ref.ProtocolID]string, len(v))
	for _, k := range slices.Sorted(maps.Keys(v)) {
		p, ok := protocolByID(k)
		if !ok {
			return nil, fmt.Errorf("%q is not a protocol id; use one of %v", k, allProtocols())
		}
		if v[k] == "" {
			return nil, fmt.Errorf("the %s message is empty", k)
		}
		out[p] = v[k]
	}
	return out, nil
}

func allProtocols() []ref.ProtocolID {
	return []ref.ProtocolID{ref.AWSJSON10, ref.AWSJSON11, ref.AWSQuery, ref.RESTJSON1, ref.RESTXML, ref.RPCv2CBOR}
}

func protocolByID(s string) (ref.ProtocolID, bool) {
	for _, p := range allProtocols() {
		if string(p) == s {
			return p, true
		}
	}
	return "", false
}

// ValidationStyle is the message grammar of a validationError rule.
type ValidationStyle uint8

// ValidationStyle values.
const (
	// StyleCoral is AWS's validation grammar (awserr.CoralMessage) over every
	// violation that maps to the same code.
	StyleCoral ValidationStyle = iota + 1
	// StyleParameter is SQS's hand text for the first violation:
	// "Value <v> for parameter <P> is invalid. Reason: <reason>".
	StyleParameter
	// StyleMissingParameter is SQS's hand text for a missing member:
	// "The request must contain the parameter <P>.".
	StyleMissingParameter
	// StyleSNSParameter is SNS's hand text for the first violation:
	// "Invalid parameter: <P> Reason: <reason>".
	StyleSNSParameter
	// StyleFixed is the reason as the whole message: a text the service
	// sends whatever failed, such as S3's MalformedXML text.
	StyleFixed
)

func validationStyles() map[string]ValidationStyle {
	return map[string]ValidationStyle{
		"coral": StyleCoral, "parameter": StyleParameter,
		"missingParameter": StyleMissingParameter, "snsParameter": StyleSNSParameter,
		"fixed": StyleFixed,
	}
}

// ConstraintKind filters the violations a validationError rule covers.
type ConstraintKind uint8

// ConstraintKind values, one per generated constraint, plus KindLengthMax,
// the length maximum alone, which a rule selects with {constraints:
// ["length"], bound: max} rather than by a constraint name.
const (
	KindRequired ConstraintKind = 1 << iota
	KindLength
	KindRange
	KindPattern
	KindEnum
	KindLengthMax
)

func constraintKinds() map[string]ConstraintKind {
	return map[string]ConstraintKind{
		"required": KindRequired, "length": KindLength, "range": KindRange, "pattern": KindPattern, "enum": KindEnum,
	}
}

// EnumPolicy says who rejects a value outside an enum.
type EnumPolicy uint8

// EnumPolicy values.
const (
	// EnumClosed: the generated validator reports an Enum violation.
	EnumClosed EnumPolicy = iota + 1
	// EnumOpen: any value is accepted.
	EnumOpen
	// EnumHandler: the service checks the value, so a business error such as
	// SQS InvalidAttributeName is not pre-empted by the generated validator.
	EnumHandler
)

func enumPolicies() map[string]EnumPolicy {
	return map[string]EnumPolicy{"closed": EnumClosed, "open": EnumOpen, "handler": EnumHandler}
}

// PatternPolicy says who rejects a member value that fails its pattern.
// Generated validators check every pattern unless a member-level rule sets
// PatternHandler.
type PatternPolicy uint8

// PatternPolicy values.
const (
	// PatternHandler: the service checks the value, so its own error, such
	// as S3's InvalidArgument "Invalid copy source object key", is not
	// pre-empted by the generated validator's Coral text.
	PatternHandler PatternPolicy = iota + 1
)

func patternPolicies() map[string]PatternPolicy {
	return map[string]PatternPolicy{"handler": PatternHandler}
}

// ValidationRule is one rule of dev.understudy#validationError. A rule with
// no Code only sets the enum or pattern policy of its target.
type ValidationRule struct {
	Code          smithy.ShapeID
	Style         ValidationStyle
	Kinds         ConstraintKind // 0 matches every kind
	Reason        string
	EnumPolicy    EnumPolicy    // 0 when the rule does not set one
	PatternPolicy PatternPolicy // 0 when the rule does not set one
	Echo          awserr.CoralEcho
}

// parseEcho reads a coral rule's value and enumSet options: what each entry
// prints of the value and of an enum's value set. "" keeps the default,
// which prints it.
func parseEcho(value, enumSet string) (awserr.CoralEcho, error) {
	var e awserr.CoralEcho
	switch value {
	case "", "echo":
	case "omit":
		e.Value = awserr.OmitValue
	default:
		return e, fmt.Errorf("value %q is not echo or omit", value)
	}
	switch enumSet {
	case "", "list":
	case "omit":
		e.Enum = awserr.OmitEnum
	default:
		return e, fmt.Errorf("enumSet %q is not list or omit", enumSet)
	}
	return e, nil
}

// parseValidationRules accepts one rule object or an array of them; rules
// are tried in order, the first whose constraint filter matches wins. At
// most one rule sets enumPolicy.
func parseValidationRules(raw json.RawMessage) ([]ValidationRule, error) {
	var objs []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := decodeTrait(raw, &objs); err != nil {
			return nil, err
		}
	} else {
		objs = []json.RawMessage{raw}
	}
	if len(objs) == 0 {
		return nil, errors.New("give at least one rule")
	}
	var out []ValidationRule
	policies := 0
	for i, o := range objs {
		r, err := parseValidationRule(o)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		if r.EnumPolicy != 0 {
			policies++
		}
		out = append(out, r)
	}
	if policies > 1 {
		return nil, errors.New("only one rule may set enumPolicy")
	}
	return out, nil
}

// parsePolicies reads a rule's enumPolicy and patternPolicy; "" leaves a
// policy unset.
func parsePolicies(enum, pattern string) (EnumPolicy, PatternPolicy, error) {
	var e EnumPolicy
	var p PatternPolicy
	if enum != "" {
		var ok bool
		if e, ok = enumPolicies()[enum]; !ok {
			return 0, 0, fmt.Errorf("enumPolicy %q is not closed, open or handler", enum)
		}
	}
	if pattern != "" {
		var ok bool
		if p, ok = patternPolicies()[pattern]; !ok {
			return 0, 0, fmt.Errorf("patternPolicy %q is not handler", pattern)
		}
	}
	return e, p, nil
}

// parseKinds reads a rule's constraints filter and its bound: "max" narrows
// a ["length"] filter to the length maximum, for a text the service sends
// only for an over-long value; "" leaves the filter as named.
func parseKinds(constraints []string, bound string) (ConstraintKind, error) {
	var kinds ConstraintKind
	for _, c := range constraints {
		k, ok := constraintKinds()[c]
		if !ok {
			return 0, fmt.Errorf("constraint %q is not one of %v", c, slices.Sorted(maps.Keys(constraintKinds())))
		}
		kinds |= k
	}
	switch bound {
	case "":
		return kinds, nil
	case "max":
		if !slices.Equal(constraints, []string{"length"}) {
			return 0, errors.New(`bound applies only to a rule whose constraints are ["length"] alone; split the other constraints into a rule of their own`)
		}
		return KindLengthMax, nil
	default:
		return 0, fmt.Errorf("bound %q is not max; leave bound out to cover both length limits", bound)
	}
}

func parseValidationRule(raw json.RawMessage) (ValidationRule, error) {
	var v struct {
		Code          string   `json:"code"`
		Style         string   `json:"style"`
		Constraints   []string `json:"constraints"`
		Bound         string   `json:"bound"`
		Reason        string   `json:"reason"`
		EnumPolicy    string   `json:"enumPolicy"`
		PatternPolicy string   `json:"patternPolicy"`
		Value         string   `json:"value"`
		EnumSet       string   `json:"enumSet"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return ValidationRule{}, err
	}
	var r ValidationRule
	var err error
	if r.EnumPolicy, r.PatternPolicy, err = parsePolicies(v.EnumPolicy, v.PatternPolicy); err != nil {
		return r, err
	}
	if r.Echo, err = parseEcho(v.Value, v.EnumSet); err != nil {
		return r, err
	}
	if v.Code == "" {
		if (r.EnumPolicy == 0 && r.PatternPolicy == 0) || v.Style != "" || len(v.Constraints) > 0 || v.Reason != "" || v.Value != "" || v.EnumSet != "" {
			return r, errors.New(`"code" is required unless the rule only sets enumPolicy or patternPolicy`)
		}
		return r, nil
	}
	code, err := smithy.Parse(v.Code)
	if err != nil {
		return r, err
	}
	r.Code = code
	r.Style = StyleCoral
	if v.Style != "" {
		s, ok := validationStyles()[v.Style]
		if !ok {
			return r, fmt.Errorf("style %q is not one of %v", v.Style, slices.Sorted(maps.Keys(validationStyles())))
		}
		r.Style = s
	}
	if r.Kinds, err = parseKinds(v.Constraints, v.Bound); err != nil {
		return r, err
	}
	r.Reason = v.Reason
	needsReason := r.Style == StyleParameter || r.Style == StyleSNSParameter || r.Style == StyleFixed
	switch {
	case needsReason && r.Reason == "":
		return r, fmt.Errorf("style %s needs a reason", v.Style)
	case !needsReason && r.Reason != "":
		return r, fmt.Errorf("style %s takes no reason", cmp.Or(v.Style, "coral"))
	case r.Style != StyleCoral && (v.Value != "" || v.EnumSet != ""):
		return r, fmt.Errorf("value and enumSet shape the coral style's entries; style %s prints none", v.Style)
	}
	return r, nil
}

// PathStyle is dev.understudy#validationPathStyle.
type PathStyle uint8

// PathStyle values.
const (
	PathIndexed  PathStyle = iota + 1 // targets.1.member.id
	PathRolledUp                      // Lambda: element constraints at the collection
)

func parsePathStyle(raw json.RawMessage) (PathStyle, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return 0, err
	}
	switch s {
	case "indexed":
		return PathIndexed, nil
	case "rolledUp":
		return PathRolledUp, nil
	default:
		return 0, fmt.Errorf("%q is not indexed or rolledUp", s)
	}
}

func parseQueryKeyAlias(raw json.RawMessage) (smithy.QueryKeyAlias, error) {
	var v struct {
		Key    string `json:"key"`
		Decode string `json:"decode"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return smithy.QueryKeyAlias{}, err
	}
	if v.Key == "" {
		return smithy.QueryKeyAlias{}, errors.New(`"key" is required`)
	}
	switch v.Decode {
	case "receive":
		return smithy.QueryKeyAlias{Key: v.Key, Decode: smithy.AliasReceive}, nil
	case "ignore":
		return smithy.QueryKeyAlias{Key: v.Key, Decode: smithy.AliasIgnore}, nil
	default:
		return smithy.QueryKeyAlias{}, fmt.Errorf(`"decode" is %q; use receive or ignore`, v.Decode)
	}
}

// EmptyCollection is dev.understudy#emptyCollection.
type EmptyCollection uint8

// EmptyCollection values.
const (
	EmptyOmit EmptyCollection = iota + 1
	EmptyEmit
)

func parseEmptyCollection(raw json.RawMessage) (EmptyCollection, error) {
	s, err := parseNonEmptyString(raw)
	if err != nil {
		return 0, err
	}
	switch s {
	case "omit":
		return EmptyOmit, nil
	case "emit":
		return EmptyEmit, nil
	default:
		return 0, fmt.Errorf("%q is not omit or emit", s)
	}
}

// PatternValidator is dev.understudy#patternValidator: an RE2 rendering of a
// lookaround pattern as patterns that must all match and patterns none of
// which may match. The violation still echoes the model's pattern.
type PatternValidator struct {
	AllOf  []string
	NoneOf []string
}

func parsePatternValidator(raw json.RawMessage) (PatternValidator, error) {
	var v struct {
		AllOf  []string `json:"allOf"`
		NoneOf []string `json:"noneOf"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return PatternValidator{}, err
	}
	if len(v.AllOf)+len(v.NoneOf) == 0 {
		return PatternValidator{}, errors.New("give allOf or noneOf patterns")
	}
	for _, p := range slices.Concat(v.AllOf, v.NoneOf) {
		if _, err := regexp.Compile(p); err != nil {
			return PatternValidator{}, fmt.Errorf("pattern %q does not compile under RE2: %w", p, err)
		}
	}
	return PatternValidator(v), nil
}

// NotConfigured is dev.understudy#notConfigured: what an S3 Get* operation
// answers while its subresource is unset. It is a NotConfiguredError or a
// NotConfiguredDefault.
type NotConfigured interface{ isNotConfigured() }

// NotConfiguredError answers with the error structure Shape.
type NotConfiguredError struct{ Shape smithy.ShapeID }

// NotConfiguredDefault answers with the output document Document.
type NotConfiguredDefault struct{ Document json.RawMessage }

func (NotConfiguredError) isNotConfigured()   {}
func (NotConfiguredDefault) isNotConfigured() {}

func parseNotConfigured(raw json.RawMessage) (NotConfigured, error) {
	var v struct {
		Error   string          `json:"error"`
		Default json.RawMessage `json:"default"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return nil, err
	}
	if (v.Error == "") == (v.Default == nil) {
		return nil, errors.New(`give exactly one of "error" and "default"`)
	}
	if v.Error == "" {
		return NotConfiguredDefault{Document: v.Default}, nil
	}
	id, err := smithy.Parse(v.Error)
	if err != nil {
		return nil, err
	}
	return NotConfiguredError{Shape: id}, nil
}

// UnknownOperation is dev.understudy#unknownOperationError: the identity per
// protocol family for an operation the model does not have.
type UnknownOperation struct {
	JSON  smithy.ShapeID
	Query smithy.ShapeID
}

func parseUnknownOperation(raw json.RawMessage) (UnknownOperation, error) {
	var v struct {
		JSON  string `json:"json"`
		Query string `json:"query"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return UnknownOperation{}, err
	}
	if v.JSON == "" && v.Query == "" {
		return UnknownOperation{}, errors.New(`give "json", "query" or both`)
	}
	var out UnknownOperation
	for _, p := range []struct {
		text string
		dst  *smithy.ShapeID
	}{{v.JSON, &out.JSON}, {v.Query, &out.Query}} {
		if p.text == "" {
			continue
		}
		id, err := smithy.Parse(p.text)
		if err != nil {
			return UnknownOperation{}, err
		}
		*p.dst = id
	}
	return out, nil
}
