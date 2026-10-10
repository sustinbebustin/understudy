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

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
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
	utValidationError     = understudyNamespace + "validationError"
	utValidationPathStyle = understudyNamespace + "validationPathStyle"
	utSuccessCode         = understudyNamespace + "successCode"
	utQueryKeyAlias       = understudyNamespace + "queryKeyAlias"
	utEmptyCollection     = understudyNamespace + "emptyCollection"
	utInternalOperation   = understudyNamespace + "internalOperation"
	utPatternEcho         = understudyNamespace + "patternEcho"
	utPatternValidator    = understudyNamespace + "patternValidator"
	utAlwaysEmit          = understudyNamespace + "alwaysEmit"
	utMaxRequestBytes     = understudyNamespace + "maxRequestBytes"
	utThrottleError       = understudyNamespace + "throttleError"
	utNotConfigured       = understudyNamespace + "notConfigured"
	utMalformedRequest    = understudyNamespace + "malformedRequestError"
	utStatusOverride      = understudyNamespace + "statusOverride"
	utUnknownOperation    = understudyNamespace + "unknownOperationError"
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
)

func (s site) String() string {
	names := map[site]string{
		siteService: "a service shape", siteOperation: "an operation shape", siteMember: "a member",
		siteErrorShape: "an error structure", siteStringShape: "a string shape", siteAnyShape: "any shape",
	}
	var out []string
	for _, b := range []site{siteService, siteOperation, siteMember, siteErrorShape, siteStringShape, siteAnyShape} {
		if s&b != 0 {
			out = append(out, names[b])
		}
	}
	return strings.Join(out, " or ")
}

// understudyTrait says where a trait may sit and how its value parses.
type understudyTrait struct {
	sites site
	parse func(json.RawMessage) error
}

func understudyTraits() map[string]understudyTrait {
	shapeRef := func(raw json.RawMessage) error { _, err := parseShapeRef(raw); return err }
	return map[string]understudyTrait{
		utEvidence:            {siteAnyShape | siteMember, func(r json.RawMessage) error { _, err := parseEvidence(r); return err }},
		utErrorNamespace:      {siteService, func(r json.RawMessage) error { _, err := parseErrorNamespace(r); return err }},
		utJSONType:            {siteErrorShape, func(r json.RawMessage) error { _, err := parseNonEmptyString(r); return err }},
		utMessageByProtocol:   {siteErrorShape, func(r json.RawMessage) error { _, err := parseMessageByProtocol(r); return err }},
		utValidationError:     {siteService | siteOperation | siteMember, func(r json.RawMessage) error { _, err := parseValidationRules(r); return err }},
		utValidationPathStyle: {siteService, func(r json.RawMessage) error { _, err := parsePathStyle(r); return err }},
		utSuccessCode:         {siteOperation, func(r json.RawMessage) error { _, err := parseStatus(r); return err }},
		utQueryKeyAlias:       {siteMember, func(r json.RawMessage) error { _, err := parseQueryKeyAlias(r); return err }},
		utEmptyCollection:     {siteService | siteMember, func(r json.RawMessage) error { _, err := parseEmptyCollection(r); return err }},
		utInternalOperation:   {siteOperation, parseAnnotation},
		utPatternEcho:         {siteStringShape, func(r json.RawMessage) error { _, err := parseNonEmptyString(r); return err }},
		utPatternValidator:    {siteStringShape, func(r json.RawMessage) error { _, err := parsePatternValidator(r); return err }},
		utAlwaysEmit:          {siteMember, parseAnnotation},
		utMaxRequestBytes:     {siteOperation, func(r json.RawMessage) error { _, err := parseMaxRequestBytes(r); return err }},
		utThrottleError:       {siteService, shapeRef},
		utNotConfigured:       {siteOperation, func(r json.RawMessage) error { _, err := parseNotConfigured(r); return err }},
		utMalformedRequest:    {siteService, shapeRef},
		utStatusOverride:      {siteErrorShape, func(r json.RawMessage) error { _, err := parseStatus(r); return err }},
		utUnknownOperation:    {siteService, func(r json.RawMessage) error { _, err := parseUnknownOperation(r); return err }},
	}
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
)

func validationStyles() map[string]ValidationStyle {
	return map[string]ValidationStyle{
		"coral": StyleCoral, "parameter": StyleParameter,
		"missingParameter": StyleMissingParameter, "snsParameter": StyleSNSParameter,
	}
}

// ConstraintKind filters the violations a validationError rule covers.
type ConstraintKind uint8

// ConstraintKind values, one per generated constraint.
const (
	KindRequired ConstraintKind = 1 << iota
	KindLength
	KindRange
	KindPattern
	KindEnum
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
	// SQS InvalidAttributeName is not pre-empted by step 7.
	EnumHandler
)

func enumPolicies() map[string]EnumPolicy {
	return map[string]EnumPolicy{"closed": EnumClosed, "open": EnumOpen, "handler": EnumHandler}
}

// ValidationRule is one rule of dev.understudy#validationError. A rule with
// no Code only sets the enum policy of its target.
type ValidationRule struct {
	Code       smithy.ShapeID
	Style      ValidationStyle
	Kinds      ConstraintKind // 0 matches every kind
	Reason     string
	EnumPolicy EnumPolicy // 0 when the rule does not set one
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

func parseValidationRule(raw json.RawMessage) (ValidationRule, error) {
	var v struct {
		Code        string   `json:"code"`
		Style       string   `json:"style"`
		Constraints []string `json:"constraints"`
		Reason      string   `json:"reason"`
		EnumPolicy  string   `json:"enumPolicy"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return ValidationRule{}, err
	}
	var r ValidationRule
	if v.EnumPolicy != "" {
		p, ok := enumPolicies()[v.EnumPolicy]
		if !ok {
			return r, fmt.Errorf("enumPolicy %q is not closed, open or handler", v.EnumPolicy)
		}
		r.EnumPolicy = p
	}
	if v.Code == "" {
		if r.EnumPolicy == 0 || v.Style != "" || len(v.Constraints) > 0 || v.Reason != "" {
			return r, errors.New(`"code" is required unless the rule only sets enumPolicy`)
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
	for _, c := range v.Constraints {
		k, ok := constraintKinds()[c]
		if !ok {
			return r, fmt.Errorf("constraint %q is not one of %v", c, slices.Sorted(maps.Keys(constraintKinds())))
		}
		r.Kinds |= k
	}
	r.Reason = v.Reason
	needsReason := r.Style == StyleParameter || r.Style == StyleSNSParameter
	switch {
	case needsReason && r.Reason == "":
		return r, fmt.Errorf("style %s needs a reason", v.Style)
	case !needsReason && r.Reason != "":
		return r, fmt.Errorf("style %s takes no reason", cmp.Or(v.Style, "coral"))
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

// QueryKeyAlias is dev.understudy#queryKeyAlias: the awsQuery key a member
// shares with another, and whether values decoded from that key land in
// this member.
type QueryKeyAlias struct {
	Key     string
	Receive bool
}

func parseQueryKeyAlias(raw json.RawMessage) (QueryKeyAlias, error) {
	var v struct {
		Key    string `json:"key"`
		Decode string `json:"decode"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return QueryKeyAlias{}, err
	}
	if v.Key == "" {
		return QueryKeyAlias{}, errors.New(`"key" is required`)
	}
	switch v.Decode {
	case "receive":
		return QueryKeyAlias{Key: v.Key, Receive: true}, nil
	case "ignore":
		return QueryKeyAlias{Key: v.Key}, nil
	default:
		return QueryKeyAlias{}, fmt.Errorf(`"decode" is %q; use receive or ignore`, v.Decode)
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
