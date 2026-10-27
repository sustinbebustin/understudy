package asl

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// AWS's diagnostic codes (api/API_ValidateStateMachineDefinitionDiagnostic,
// stepfunctions-asl.md 3.6).
const (
	codeInvalidJSON      = "INVALID_JSON_DESCRIPTION"
	codeSchema           = "SCHEMA_VALIDATION_FAILED"
	codeMissingTarget    = "MISSING_TRANSITION_TARGET"
	codeMissingEndState  = "MISSING_END_STATE"
	codeInvalidStateName = "INVALID_STATE_NAME"
)

const (
	unsupportedSuffix     = " Run 'understudy coverage --service sfn' for what is supported."
	maxStateNameRunes     = 80
	queryLanguageJSONPath = "JSONPath"
	queryLanguageJSONata  = "JSONata"
	typePass              = "Pass"
	typeSucceed           = "Succeed"
)

// stateTypes are the values of a state's Type, in the order AWS's message
// lists them (stepfunctions-asl.md 3.5, recorded).
func stateTypes() []string {
	return []string{"Task", "Wait", typePass, typeSucceed, "Fail", "Choice", "Parallel", "Map"}
}

// refusedField names the construct of a state field understudy refuses
// whatever the features: JSONata's fields, variables, task credentials and
// heartbeats, and Distributed Map's fields (spec "Definition grammar").
func refusedField(field string) (string, bool) {
	switch field {
	case "Arguments", "Output", "Items":
		return "JSONata (the " + field + " field)", true
	case "Assign":
		return "variables (the Assign field)", true
	case "Credentials":
		return "task Credentials", true
	case "HeartbeatSeconds", "HeartbeatSecondsPath":
		return "task heartbeats (" + field + ")", true
	case "ItemReader", "ItemBatcher", "ResultWriter", "ToleratedFailurePercentage", "ToleratedFailurePercentagePath",
		"ToleratedFailureCount", "ToleratedFailureCountPath", "Label":
		return "Distributed Map (" + field + ")", true
	}
	return "", false
}

// typeFields are the fields a JSONPath state type takes
// (stepfunctions-asl.md 3.3); a field outside them is a schema error.
func typeFields(typ string) []string {
	switch typ {
	case typePass:
		return []string{"Type", "Comment", "QueryLanguage", "Next", "End", "InputPath", "OutputPath", "Parameters", "ResultPath", "Result", "Assign"}
	case typeSucceed:
		return []string{"Type", "Comment", "QueryLanguage", "InputPath", "OutputPath"}
	}
	return nil
}

// builtFields are the fields of a state type whose runtime has landed; a
// field the type takes outside them is refused as not implemented.
func builtFields(typ string) []string {
	switch typ {
	case typePass:
		return []string{"Type", "Comment", "QueryLanguage", "Next", "End", "Result"}
	case typeSucceed:
		return []string{"Type", "Comment", "QueryLanguage"}
	}
	return nil
}

// typeFeature is the feature a built state type needs; false for a type
// whose runtime has not landed.
func typeFeature(typ string) (Feature, bool) {
	switch typ {
	case typePass:
		return FeatPass, true
	case typeSucceed:
		return FeatSucceed, true
	}
	return 0, false
}

// Parse parses definition, accepting the constructs of accept. It returns
// every diagnostic in document order; the Definition is nil when any
// diagnostic is SeverityError. A construct whose feature is outside accept,
// or whose runtime has not landed, is an ERROR with code CodeUnsupported.
func Parse(definition string, accept Features) (*Definition, []Diagnostic) {
	p := parser{accept: accept}
	d := p.definition(definition)
	for _, diag := range p.diags {
		if diag.Severity == SeverityError {
			return nil, p.diags
		}
	}
	return d, p.diags
}

// parser collects the diagnostics of one Parse.
type parser struct {
	accept Features
	diags  []Diagnostic
}

func (p *parser) fail(code, message, location string) {
	p.diags = append(p.diags, Diagnostic{Severity: SeverityError, Code: code, Message: message, Location: location})
}

// unsupported refuses construct at location in the stepfunctions-asl.md
// 10.4 message form.
func (p *parser) unsupported(construct, location string) {
	p.fail(CodeUnsupported, "understudy does not implement "+construct+"."+unsupportedSuffix, location)
}

// topLevel is the parsed top-level object.
func (p *parser) topLevel(text string) (document.Map, bool) {
	if strings.TrimSpace(text) == "" {
		// Recorded for " " (test_sfn_api_validation.snapshot.json:L2).
		p.fail(codeSchema, "Definition must be a valid JSON object", "")
		return nil, false
	}
	dec := jsonx.NewDecoder([]byte(text))
	v, err := dec.ReadValue()
	if err == nil {
		err = dec.End()
	}
	if err != nil {
		// AWS answers Jackson's parser text here (stepfunctions-asl.md 3.6);
		// this wording is understudy's (UNVERIFIED, ASL-01).
		p.fail(codeInvalidJSON, "The definition is not valid JSON: "+err.Error(), "")
		return nil, false
	}
	top, ok := v.(document.Map)
	if !ok {
		p.fail(codeSchema, "Definition must be a valid JSON object", "")
		return nil, false
	}
	var missing []string
	for _, f := range []string{"States", "StartAt"} {
		if _, ok := member(top, f); !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		// Recorded order: [States, StartAt] (test_sfn_api_validation.snapshot.json:L43).
		p.fail(codeSchema, "These fields are required: ["+strings.Join(missing, ", ")+"]", "/")
		return nil, false
	}
	return top, true
}

func (p *parser) definition(text string) *Definition {
	top, ok := p.topLevel(text)
	if !ok {
		return nil
	}
	d := &Definition{states: map[string]state{}}
	statesValue, _ := member(top, "States")
	states, statesOK := statesValue.(document.Map)
	names := map[string]bool{}
	for _, e := range states {
		names[e.Key] = true
	}
	for _, e := range top {
		switch e.Key {
		case "StartAt":
			d.startAt = p.startAt(e.Value, statesOK, names)
		case "States":
			if !statesOK {
				p.fail(codeSchema, "The field 'States' must be an object", "/States")
				continue
			}
			p.states(d, states, names)
		case "Comment":
			p.str(e.Value, "Comment", "/Comment")
		case "Version":
			if s, isString := e.Value.(document.String); !isString || s != "1.0" {
				p.fail(codeSchema, "The field 'Version' should have one of these values: [1.0]", "/Version")
			}
		case "QueryLanguage":
			p.queryLanguage(e.Value, "/")
		case "TimeoutSeconds":
			p.unsupported("the top-level TimeoutSeconds field yet", "/TimeoutSeconds")
		default:
			p.fail(codeSchema, "Field '"+e.Key+"' is not supported at the top level", "/")
		}
	}
	return d
}

// startAt checks StartAt names a state of the top-level States.
func (p *parser) startAt(v document.Value, statesOK bool, names map[string]bool) string {
	s, ok := v.(document.String)
	switch {
	case !ok:
		p.fail(codeSchema, "The field 'StartAt' must be a string", "/StartAt")
	case statesOK && !names[string(s)]:
		p.fail(codeMissingTarget, "Missing 'StartAt' target: "+string(s), "/StartAt")
	}
	return string(s)
}

// queryLanguage checks a QueryLanguage value; at is "/" for the top level,
// where the refusal's location is "/" itself (spec Done checks), and the
// state's location otherwise.
func (p *parser) queryLanguage(v document.Value, at string) {
	field := at + "/QueryLanguage"
	if at == "/" {
		field = "/"
	}
	s, ok := v.(document.String)
	switch {
	case ok && s == queryLanguageJSONPath:
	case ok && s == queryLanguageJSONata:
		p.unsupported(`JSONata; QueryLanguage "JSONata" is refused`, field)
	default:
		p.fail(codeSchema, "The field 'QueryLanguage' should have one of these values: [JSONPath, JSONata]", field)
	}
}

func (p *parser) str(v document.Value, field, location string) (string, bool) {
	s, ok := v.(document.String)
	if !ok {
		p.fail(codeSchema, "The field '"+field+"' must be a string", location)
	}
	return string(s), ok
}

// states parses the States object of one scope; names are its state names.
func (p *parser) states(d *Definition, states document.Map, names map[string]bool) {
	terminal := false
	for _, e := range states {
		loc := "/States/" + e.Key
		if e.Key == "" || utf8.RuneCountInString(e.Key) > maxStateNameRunes {
			p.fail(codeInvalidStateName, "State names must be 1 to 80 characters", loc)
			continue
		}
		obj, ok := e.Value.(document.Map)
		if !ok {
			p.fail(codeSchema, "A state must be an object", loc)
			continue
		}
		s, ends := p.state(e.Key, obj, names)
		terminal = terminal || ends
		if s != nil {
			d.states[e.Key] = s
		}
	}
	if len(states) > 0 && !terminal && len(d.states) == len(states) {
		p.fail(codeMissingEndState, "No terminal state found in the machine", "/States")
	}
}

// stateFields are the built fields read from one state.
type stateFields struct {
	next          string
	hasNext, ends bool
	result        document.Value
}

// state parses one state. It returns nil when the state has an error, and
// whether the state ends the machine.
func (p *parser) state(name string, obj document.Map, names map[string]bool) (state, bool) {
	loc := "/States/" + name
	typeValue, ok := member(obj, "Type")
	if !ok {
		p.fail(codeSchema, "These fields are required: [Type]", loc)
		return nil, false
	}
	typ, isString := typeValue.(document.String)
	if !isString || !slices.Contains(stateTypes(), string(typ)) {
		// Recorded (stepfunctions-asl.md 3.5).
		p.fail(codeSchema, "The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map]", loc+"/Type")
		return nil, false
	}
	if feature, built := typeFeature(string(typ)); !built || !p.accept.Has(feature) {
		p.unsupported(string(typ)+" states", loc)
		return nil, false
	}
	before := len(p.diags)
	var f stateFields
	for _, e := range obj {
		p.field(string(typ), loc, e, names, &f)
	}
	if typ == typePass {
		switch {
		case f.hasNext && f.ends:
			p.fail(codeSchema, "Only one of 'Next' or 'End' is allowed", loc)
		case !f.hasNext && !f.ends:
			p.fail(codeSchema, "One of 'Next' or 'End' is required", loc)
		}
	}
	ends := f.ends || typ == typeSucceed
	if len(p.diags) > before {
		return nil, ends
	}
	if typ == typePass {
		return passState{name: name, next: f.next, result: f.result}, ends
	}
	return succeedState{name: name}, ends
}

// field reads one field of a state of type typ at loc into f.
func (p *parser) field(typ, loc string, e document.Entry, names map[string]bool, f *stateFields) {
	at := loc + "/" + e.Key
	if construct, refused := refusedField(e.Key); refused {
		p.unsupported(construct, at)
		return
	}
	if !slices.Contains(typeFields(typ), e.Key) {
		p.fail(codeSchema, "Field '"+e.Key+"' is not supported by a "+typ+" state", at)
		return
	}
	if !slices.Contains(builtFields(typ), e.Key) {
		p.unsupported("the "+e.Key+" field yet", at)
		return
	}
	switch e.Key {
	case "Comment":
		p.str(e.Value, e.Key, at)
	case "QueryLanguage":
		p.queryLanguage(e.Value, loc)
	case "Next":
		f.hasNext = true
		if s, ok := p.str(e.Value, e.Key, at); ok {
			if !names[s] {
				p.fail(codeMissingTarget, "Missing 'Next' target: "+s, at)
			}
			f.next = s
		}
	case "End":
		if b, ok := e.Value.(document.Bool); !ok || !bool(b) {
			p.fail(codeSchema, "The field 'End' must be true", at)
		}
		f.ends = true
	case "Result":
		f.result = e.Value
	}
}

func member(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}
