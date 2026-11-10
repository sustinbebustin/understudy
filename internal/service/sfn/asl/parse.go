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
	codeDuplicateName    = "DUPLICATE_STATE_NAME"
)

const (
	unsupportedSuffix     = " Run 'understudy coverage --service sfn' for what is supported."
	maxStateNameRunes     = 80
	queryLanguageJSONPath = "JSONPath"
	queryLanguageJSONata  = "JSONata"
	typeTask              = "Task"
	typeWait              = "Wait"
	typePass              = "Pass"
	typeSucceed           = "Succeed"
	typeFail              = "Fail"
	typeChoice            = "Choice"
	typeParallel          = "Parallel"
	typeMap               = "Map"
	// maxSeconds is the largest TimeoutSeconds, Seconds and IntervalSeconds
	// (stepfunctions-asl.md 3.7, aws-doc).
	maxSeconds = 99_999_999
)

// stateTypes are the values of a state's Type, in the order AWS's message
// lists them (stepfunctions-asl.md 3.5, recorded).
func stateTypes() []string {
	return []string{typeTask, typeWait, typePass, typeSucceed, typeFail, typeChoice, typeParallel, typeMap}
}

// typeFields are the fields a JSONPath state of type typ takes besides
// Type, Comment and QueryLanguage (stepfunctions-asl.md 3.3), and the
// fields of AWS's grammar understudy refuses on it: JSONata's fields,
// Assign, task credentials and heartbeats, Distributed Map's fields. A
// field in neither list is a schema error.
func typeFields(typ string) (taken, refused []string) {
	io := []string{"InputPath", "OutputPath"}
	flow := []string{"Next", "End"}
	result := []string{"ResultSelector", "ResultPath", "Retry", "Catch"}
	switch typ {
	case typePass:
		return slices.Concat(io, flow, []string{"Parameters", "ResultPath", "Result"}), []string{"Assign", "Output"}
	case typeTask:
		return slices.Concat(io, flow, result, []string{"Parameters", "Resource", "TimeoutSeconds", "TimeoutSecondsPath"}),
			[]string{"Assign", "Output", "Arguments", "HeartbeatSeconds", "HeartbeatSecondsPath", "Credentials"}
	case typeChoice:
		return slices.Concat(io, []string{"Choices", "Default"}), []string{"Assign", "Output"}
	case typeWait:
		return slices.Concat(io, flow, []string{"Seconds", "SecondsPath", "Timestamp", "TimestampPath"}), []string{"Assign", "Output"}
	case typeSucceed:
		return io, []string{"Output"}
	case typeFail:
		return []string{"Error", "ErrorPath", "Cause", "CausePath"}, nil
	case typeParallel:
		return slices.Concat(io, flow, result, []string{"Parameters", "Branches"}), []string{"Assign", "Output", "Arguments"}
	case typeMap:
		return slices.Concat(io, flow, result, []string{"Parameters", "ItemSelector", "ItemProcessor", "Iterator", "ItemsPath", "MaxConcurrency", "MaxConcurrencyPath"}),
			[]string{
				"Assign", "Output", "Items", "ItemReader", "ItemBatcher", "ResultWriter", "ToleratedFailurePercentage",
				"ToleratedFailurePercentagePath", "ToleratedFailureCount", "ToleratedFailureCountPath", "Label",
			}
	}
	return nil, nil
}

// refusedConstruct names a refused field in the stepfunctions-asl.md 10.4
// message.
func refusedConstruct(field string) string {
	switch field {
	case "Arguments", "Output", "Items", "Condition":
		return "JSONata (the " + field + " field)"
	case "Assign":
		return "variables (the Assign field)"
	case "Credentials":
		return "task Credentials"
	case "HeartbeatSeconds", "HeartbeatSecondsPath":
		return "task heartbeats (" + field + ")"
	}
	return "Distributed Map (" + field + ")"
}

// typeFeature is the feature a state type needs.
func typeFeature(typ string) Feature {
	switch typ {
	case typePass:
		return FeatPass
	case typeSucceed:
		return FeatSucceed
	case typeFail:
		return FeatFail
	case typeChoice:
		return FeatChoice
	case typeWait:
		return FeatWait
	case typeTask:
		return FeatLambdaTask
	case typeParallel:
		return FeatParallel
	}
	return FeatMap
}

// Parse parses definition, accepting the constructs of accept. It returns
// every diagnostic in document order, the transition checks
// (MISSING_TRANSITION_TARGET, MISSING_END_STATE) after the rest, as AWS
// orders them (ASL-01, recorded 2026-11-03); the Definition is nil when any
// diagnostic is SeverityError. A construct whose feature is outside accept
// is an ERROR with code CodeUnsupported, as is every construct understudy
// refuses whatever the features.
func Parse(definition string, accept Features) (*Definition, []Diagnostic) {
	p := parser{accept: accept, seen: map[string]bool{}}
	d := p.definition(definition)
	slices.SortStableFunc(p.diags, func(a, b Diagnostic) int {
		return transitionCheck(a) - transitionCheck(b)
	})
	for _, diag := range p.diags {
		if diag.Severity == SeverityError {
			return nil, p.diags
		}
	}
	return d, p.diags
}

// transitionCheck is 1 for a diagnostic of the transition checks, which
// AWS reports after the others, and 0 otherwise.
func transitionCheck(d Diagnostic) int {
	if d.Code == codeMissingTarget || d.Code == codeMissingEndState {
		return 1
	}
	return 0
}

// parser collects the diagnostics of one Parse and the states it builds.
type parser struct {
	accept Features
	diags  []Diagnostic
	// quiet is above 0 inside a state whose type is already refused: its
	// fields are still checked against AWS's rules, but their own
	// refusals would only repeat the state's.
	quiet  int
	seen   map[string]bool
	states map[string]state
}

func (p *parser) fail(code, message, location string) {
	p.diags = append(p.diags, Diagnostic{Severity: SeverityError, Code: code, Message: message, Location: location})
}

// schema reports a SCHEMA_VALIDATION_FAILED problem.
func (p *parser) schema(message, location string) { p.fail(codeSchema, message, location) }

// unknownField reports a field the grammar has no place for in the object
// at location: AWS names the field and points at the object (ASL-01,
// recorded 2026-11-03 for states; inferred for the other objects).
func (p *parser) unknownField(field, location string) {
	p.schema("Field '"+field+"' is not supported", location)
}

// unsupported refuses construct at location in the stepfunctions-asl.md
// 10.4 message form.
func (p *parser) unsupported(construct, location string) {
	if p.quiet > 0 {
		return
	}
	p.fail(CodeUnsupported, "understudy does not implement "+construct+"."+unsupportedSuffix, location)
}

// need refuses construct at location unless f is accepted; it reports
// whether f is accepted.
func (p *parser) need(f Feature, construct, location string) bool {
	if p.accept.Has(f) {
		return true
	}
	p.unsupported(construct+" yet", location)
	return false
}

// errorsSince reports whether an ERROR was added after the first n
// diagnostics.
func (p *parser) errorsSince(n int) bool {
	return slices.ContainsFunc(p.diags[n:], func(d Diagnostic) bool { return d.Severity == SeverityError })
}

// topLevel is the parsed top-level object.
func (p *parser) topLevel(text string) (document.Map, bool) {
	if strings.TrimSpace(text) == "" {
		// Recorded for " " (test_sfn_api_validation.snapshot.json:L2).
		p.schema("Definition must be a valid JSON object", "")
		return nil, false
	}
	dec := jsonx.NewDecoder([]byte(text))
	v, err := dec.ReadValue()
	if err == nil {
		err = dec.End()
	}
	if err != nil {
		message, location := jsonError(text, err)
		p.fail(codeInvalidJSON, message, location)
		return nil, false
	}
	top, ok := v.(document.Map)
	if !ok {
		p.schema("Definition must be a valid JSON object", "")
		return nil, false
	}
	return top, p.required(top, "/")
}

// required checks a scope object has States and StartAt; the recorded
// order is [States, StartAt] (test_sfn_api_validation.snapshot.json:L43).
func (p *parser) required(obj document.Map, location string) bool {
	var missing []string
	for _, f := range []string{"States", "StartAt"} {
		if _, ok := member(obj, f); !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		p.schema("These fields are required: ["+strings.Join(missing, ", ")+"]", location)
		return false
	}
	return true
}

func (p *parser) definition(text string) *Definition {
	top, ok := p.topLevel(text)
	if !ok {
		return nil
	}
	p.states = map[string]state{}
	d := &Definition{states: p.states}
	scoped := false
	for _, e := range top {
		switch e.Key {
		case "StartAt", "States":
			// The scope is read once, where the first of the two appears.
			if !scoped {
				d.startAt = p.scope(top, "")
				scoped = true
			}
		case "Comment":
			p.str(e.Value, "Comment", "/Comment")
		case "Version":
			if s, isString := e.Value.(document.String); !isString || s != "1.0" {
				p.schema("The field 'Version' should have one of these values: [1.0]", "/Version")
			}
		case "QueryLanguage":
			p.queryLanguage(e.Value, "/")
		case "TimeoutSeconds":
			if p.need(FeatTimeout, "the top-level TimeoutSeconds field", "/TimeoutSeconds") {
				d.timeoutSeconds, _ = p.integer(e.Value, "TimeoutSeconds", "/TimeoutSeconds", 1, maxSeconds)
			}
		default:
			p.unknownField(e.Key, "/")
		}
	}
	return d
}

// scope parses the StartAt and States of obj, a machine, a Parallel branch
// or a Map processor at prefix ("" for the machine), and returns StartAt.
// Transitions stay inside the scope (stepfunctions-asl.md 3.2).
func (p *parser) scope(obj document.Map, prefix string) string {
	startValue, _ := member(obj, "StartAt")
	statesValue, _ := member(obj, "States")
	states, statesOK := statesValue.(document.Map)
	if !statesOK {
		p.schema("The field 'States' must be an object", prefix+"/States")
	}
	names := map[string]bool{}
	for _, e := range states {
		names[e.Key] = true
	}
	start, ok := startValue.(document.String)
	switch {
	case !ok:
		p.schema("The field 'StartAt' must be a string", prefix+"/StartAt")
	case statesOK && !names[string(start)]:
		p.fail(codeMissingTarget, "Missing 'StartAt' target: "+string(start), prefix+"/StartAt")
	}
	terminal := false
	for _, e := range states {
		terminal = p.namedState(e, prefix, names) || terminal
	}
	if ok && names[string(start)] {
		p.unreachable(states, string(start), prefix)
	}
	if len(states) > 0 && !terminal {
		// Recorded without a location, beside the scope's other problems
		// (ASL-01, 2026-11-03).
		p.fail(codeMissingEndState, "Workflow has no terminal state", "")
	}
	return string(start)
}

// unreachable reports each state of the scope no transition from start
// reaches, as AWS does (ASL-01, recorded 2026-11-03).
func (p *parser) unreachable(states document.Map, start, prefix string) {
	reached := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		obj, _ := member(states, queue[0])
		queue = queue[1:]
		for _, next := range targets(obj) {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for _, e := range states {
		if !reached[e.Key] {
			p.fail(codeMissingTarget, `State "`+e.Key+`" is not reachable.`, prefix+"/States/"+e.Key)
		}
	}
}

// endsAsWritten reports whether a state object, unparsed, ends its scope:
// End true, or a Succeed or Fail type.
func endsAsWritten(v document.Value) bool {
	obj, isMap := v.(document.Map)
	if !isMap {
		return false
	}
	end, _ := member(obj, "End")
	typ, _ := member(obj, "Type")
	return end == document.Bool(true) || typ == document.String(typeSucceed) || typ == document.String(typeFail)
}

// targets are the states a state's Next, Default, Choices and Catch name.
func targets(v document.Value) []string {
	obj, isMap := v.(document.Map)
	if !isMap {
		return nil
	}
	var out []string
	add := func(m document.Map, field string) {
		if s, ok := member(m, field); ok {
			if name, isString := s.(document.String); isString {
				out = append(out, string(name))
			}
		}
	}
	add(obj, "Next")
	add(obj, "Default")
	for _, field := range []string{"Choices", "Catch"} {
		list, _ := member(obj, field)
		items, isList := list.(document.List)
		if !isList {
			continue
		}
		for _, x := range items {
			if m, isMap := x.(document.Map); isMap {
				add(m, "Next")
			}
		}
	}
	return out
}

// namedState checks a state's name and parses it; it reports whether the
// state ends its scope.
func (p *parser) namedState(e document.Entry, prefix string, names map[string]bool) bool {
	loc := prefix + "/States/" + e.Key
	// A state refused for its name still counts for MISSING_END_STATE: AWS
	// reported a duplicate or too long name alone (ASL-01, recorded
	// 2026-11-03).
	switch {
	case e.Key == "":
		// AWS's message for an empty name is unrecorded (ASL-01).
		p.fail(codeInvalidStateName, "State names must be 1 to 80 characters", loc)
		return endsAsWritten(e.Value)
	case utf8.RuneCountInString(e.Key) > maxStateNameRunes:
		p.fail(codeInvalidStateName, "Invalid State name: State '"+e.Key+"' exceeds the 80-character limit allowed by the service", loc)
		return endsAsWritten(e.Value)
	case p.seen[e.Key]:
		// At the later state.
		p.fail(codeDuplicateName, "Duplicate State name: "+e.Key, loc)
		return endsAsWritten(e.Value)
	}
	p.seen[e.Key] = true
	obj, ok := e.Value.(document.Map)
	if !ok {
		p.schema("A state must be an object", loc)
		return false
	}
	s, ends := p.state(e.Key, obj, loc, names)
	if s != nil {
		p.states[e.Key] = s
	}
	return ends
}

// queryLanguage checks a QueryLanguage value; at is "/" for the top level,
// where the refusal's location is "/" itself, and the
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
		p.schema("The field 'QueryLanguage' should have one of these values: [JSONPath, JSONata]", field)
	}
}

func (p *parser) str(v document.Value, field, location string) (string, bool) {
	s, ok := v.(document.String)
	if !ok {
		p.schema("The field '"+field+"' must be a string", location)
	}
	return string(s), ok
}

// state parses one state at loc. It returns nil when the state has an
// error, and whether the state ends its scope.
func (p *parser) state(name string, obj document.Map, loc string, names map[string]bool) (state, bool) {
	typeValue, ok := member(obj, "Type")
	if !ok {
		p.schema("These fields are required: [Type]", loc)
		return nil, false
	}
	typ, isString := typeValue.(document.String)
	if !isString || !slices.Contains(stateTypes(), string(typ)) {
		// Recorded (stepfunctions-asl.md 3.5).
		p.schema("The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map]", loc+"/Type")
		return nil, false
	}
	if !p.need(typeFeature(string(typ)), string(typ)+" states", loc) {
		p.quiet++
		defer func() { p.quiet-- }()
	}
	before := len(p.diags)
	f := draft{typ: string(typ), name: name, loc: loc, names: names}
	taken, refused := typeFields(string(typ))
	for _, e := range obj {
		at := loc + "/" + e.Key
		switch {
		case e.Key == "Type":
		case e.Key == "Comment":
			p.str(e.Value, e.Key, at)
		case e.Key == "QueryLanguage":
			p.queryLanguage(e.Value, loc)
		case slices.Contains(refused, e.Key):
			p.unsupported(refusedConstruct(e.Key), at)
		case slices.Contains(taken, e.Key):
			p.field(&f, e, at)
		default:
			p.unknownField(e.Key, loc)
		}
	}
	s := p.build(string(typ), &f)
	ends := f.end || typ == typeSucceed || typ == typeFail
	if p.errorsSince(before) {
		return nil, ends
	}
	return s, ends
}

func member(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}
