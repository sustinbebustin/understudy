package asl

import (
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// draft collects the fields of one state as they are read, in document
// order; build then checks the combinations and makes the state.
type draft struct {
	typ, name, loc string
	// names are the state names of the state's scope: the targets its
	// transitions may name.
	names map[string]bool
	// fields are the fields read, valid or not.
	fields []string

	next           string
	end            bool
	io             paths
	parameters     template
	itemSelector   template
	resultSelector template
	resultPath     target
	result         document.Value
	retry          []Retrier
	catch          []catcher

	resource ResourceKind
	function string
	timeout  timeout
	until    wait
	error    message
	cause    message

	choices   []choiceRule
	otherwise string
	branches  []string
	processor string
	itemsPath jsonpath.Path
	limit     concurrency
}

func (f *draft) has(fields ...string) bool {
	return slices.ContainsFunc(fields, func(x string) bool { return slices.Contains(f.fields, x) })
}

// count is how many of fields were read.
func (f *draft) count(fields ...string) int {
	n := 0
	for _, x := range fields {
		if slices.Contains(f.fields, x) {
			n++
		}
	}
	return n
}

// field reads one field the state's type takes.
func (p *parser) field(f *draft, e document.Entry, at string) {
	f.fields = append(f.fields, e.Key)
	switch e.Key {
	case "Next":
		f.next = p.transition(e.Value, "Next", at, f.names)
	case "End":
		if b, ok := e.Value.(document.Bool); !ok || !bool(b) {
			p.schema("The field 'End' must be true", at)
		}
		f.end = true
	case "InputPath", "OutputPath":
		if p.need(FeatPayloadTemplates, "the "+e.Key+" field", at) {
			if s, ok := p.selector(e.Value, e.Key, at); ok && e.Key == "InputPath" {
				f.io.input = s
			} else if ok {
				f.io.output = s
			}
		}
	case "Parameters", "ItemSelector", "ResultSelector":
		p.templateField(f, e, at)
	case "ResultPath":
		if p.need(FeatPayloadTemplates, "the ResultPath field", at) {
			f.resultPath = p.target(e.Value, at)
		}
	case "Result":
		f.result = e.Value
	case "Retry":
		if p.need(FeatRetryCatch, "the Retry field", at) {
			f.retry = p.retriers(e.Value, at)
		}
	case "Catch":
		if p.need(FeatRetryCatch, "the Catch field", at) {
			f.catch = p.catchers(e.Value, at, f.names)
		}
	default:
		p.typeField(f, e, at)
	}
}

// templateField reads Parameters, ItemSelector or ResultSelector. A Map's
// ItemSelector and Parameters come with FeatMap; elsewhere they are payload
// templates.
func (p *parser) templateField(f *draft, e document.Entry, at string) {
	mapSelector := f.typ == typeMap && e.Key != "ResultSelector"
	if !mapSelector && !p.need(FeatPayloadTemplates, "the "+e.Key+" field", at) {
		return
	}
	t := p.template(e.Value, e.Key, at)
	switch e.Key {
	case "ResultSelector":
		f.resultSelector = t
	case "ItemSelector":
		f.itemSelector = t
	default:
		f.parameters = t
	}
}

// typeField reads a field particular to one state type.
func (p *parser) typeField(f *draft, e document.Entry, at string) {
	switch e.Key {
	case "Resource":
		f.resource, f.function = p.resource(e.Value, at)
	case "TimeoutSeconds":
		if n, ok := p.integer(e.Value, e.Key, at, 1, maxSeconds); ok {
			f.timeout = timeoutSeconds{seconds: n}
		}
	case "TimeoutSecondsPath":
		if path, ok := p.refPath(e.Value, e.Key, at, refOptions{}); ok {
			f.timeout = timeoutPath{path: path}
		}
	case "Seconds":
		if n, ok := p.integer(e.Value, e.Key, at, 0, maxSeconds); ok {
			f.until = waitSeconds{seconds: n}
		}
	case "SecondsPath":
		if path, ok := p.refPath(e.Value, e.Key, at, refOptions{}); ok {
			f.until = waitSecondsPath{path: path}
		}
	case "Timestamp":
		if t, ok := p.timestamp(e.Value, at); ok {
			f.until = waitTimestamp{at: t}
		}
	case "TimestampPath":
		if path, ok := p.refPath(e.Value, e.Key, at, refOptions{}); ok {
			f.until = waitTimestampPath{path: path}
		}
	case "Error", "Cause":
		if s, ok := p.str(e.Value, e.Key, at); ok {
			p.setMessage(f, e.Key, literalMessage{text: s})
		}
	case "ErrorPath", "CausePath":
		if m := p.messagePath(e.Value, e.Key, at); m != nil {
			p.setMessage(f, e.Key, m)
		}
	default:
		p.flowField(f, e, at)
	}
}

// flowField reads a field of Choice, Parallel or Map.
func (p *parser) flowField(f *draft, e document.Entry, at string) {
	switch e.Key {
	case "Choices":
		f.choices = p.choices(e.Value, at, f.names)
	case "Default":
		f.otherwise = p.transition(e.Value, "Default", at, f.names)
	case "Branches":
		f.branches = p.branches(e.Value, at)
	case "ItemProcessor", "Iterator":
		f.processor = p.processor(e.Value, e.Key, at)
	case "ItemsPath":
		if path, ok := p.refPath(e.Value, e.Key, at, refOptions{}); ok {
			f.itemsPath = path
		}
	case "MaxConcurrency":
		if n, ok := p.integer(e.Value, e.Key, at, 0, maxSeconds); ok {
			f.limit = maxConcurrency{n: n}
		}
	case "MaxConcurrencyPath":
		if path, ok := p.refPath(e.Value, e.Key, at, refOptions{}); ok {
			f.limit = maxConcurrencyPath{path: path}
		}
	}
}

func (p *parser) setMessage(f *draft, field string, m message) {
	if field == "Error" || field == "ErrorPath" {
		f.error = m
		return
	}
	f.cause = m
}

// transition reads a Next or Default and checks it names a state of the
// scope.
func (p *parser) transition(v document.Value, field, at string, names map[string]bool) string {
	s, ok := p.str(v, field, at)
	if ok && !names[s] {
		p.fail(codeMissingTarget, "Missing '"+field+"' target: "+s, at)
	}
	return s
}

// exclusive reports a state that has more than one of fields, in AWS's
// words (ASL-01, recorded 2026-11-03 for Next and End; inferred for the
// other pairs).
func (p *parser) exclusive(f *draft, fields ...string) {
	if f.count(fields...) > 1 {
		p.schema("There should only be one of the following fields: ["+strings.Join(fields, ", ")+"]", f.loc)
	}
}

// build checks the combinations of fields a type requires and makes the
// state.
func (p *parser) build(typ string, f *draft) state {
	switch typ {
	case typePass, typeTask, typeWait, typeParallel, typeMap:
		p.exclusive(f, "Next", "End")
		if !f.has("Next", "End") {
			p.schema("One of 'Next' or 'End' is required", f.loc)
		}
	}
	io := f.io
	if io.input == nil {
		io.input = selectAll{}
	}
	if io.output == nil {
		io.output = selectAll{}
	}
	resultPath := f.resultPath
	if resultPath == nil {
		resultPath = replaceInput{}
	}
	switch typ {
	case typePass:
		return passState{name: f.name, next: f.next, io: io, parameters: f.parameters, result: f.result, resultPath: resultPath}
	case typeSucceed:
		return succeedState{name: f.name, io: io}
	case typeFail:
		return p.buildFail(f)
	case typeChoice:
		if !f.has("Choices") {
			p.schema("These fields are required: [Choices]", f.loc)
		}
		return choiceState{name: f.name, io: io, choices: f.choices, otherwise: f.otherwise}
	case typeWait:
		if !f.has("Seconds", "SecondsPath", "Timestamp", "TimestampPath") {
			p.schema("One of 'Seconds', 'SecondsPath', 'Timestamp' or 'TimestampPath' is required", f.loc)
		}
		p.exclusive(f, "Seconds", "SecondsPath", "Timestamp", "TimestampPath")
		return waitState{name: f.name, next: f.next, io: io, until: f.until}
	case typeTask:
		return p.buildTask(f, io, resultPath)
	case typeParallel:
		if !f.has("Branches") {
			p.schema("These fields are required: [Branches]", f.loc)
		}
		return parallelState{
			name: f.name, next: f.next, io: io, parameters: f.parameters, resultSelector: f.resultSelector,
			resultPath: resultPath, branches: f.branches, retry: f.retry, catch: f.catch,
		}
	}
	return p.buildMap(f, io, resultPath)
}

func (p *parser) buildFail(f *draft) state {
	p.exclusive(f, "Error", "ErrorPath")
	p.exclusive(f, "Cause", "CausePath")
	s := failState{name: f.name, error: f.error, cause: f.cause}
	if s.error == nil {
		s.error = noMessage{}
	}
	if s.cause == nil {
		s.cause = noMessage{}
	}
	return s
}

func (p *parser) buildTask(f *draft, io paths, resultPath target) state {
	if !f.has("Resource") {
		p.schema("These fields are required: [Resource]", f.loc)
	}
	p.exclusive(f, "TimeoutSeconds", "TimeoutSecondsPath")
	if f.resource == LambdaInvoke {
		p.invokeParameters(f)
	}
	s := taskState{
		name: f.name, next: f.next, io: io, resource: f.resource, function: f.function, parameters: f.parameters,
		resultSelector: f.resultSelector, resultPath: resultPath, timeout: f.timeout, retry: f.retry, catch: f.catch,
	}
	if s.timeout == nil {
		s.timeout = defaultTimeout{}
	}
	return s
}

// invokeParameters checks the Parameters of arn:aws:states:::lambda:invoke:
// FunctionName, which Lambda's Invoke requires, is required at create
// (understudy's choice and wording, modelled on the recorded Credentials
// RoleArn text of stepfunctions-asl.md 3.3; ASL-01), and a literal
// InvocationType other than RequestResponse is refused; a path-valued one
// is checked at run time (SFN-13).
func (p *parser) invokeParameters(f *draft) {
	at := f.loc + "/Parameters"
	if !f.has("Parameters") {
		p.schema("The field 'FunctionName' is required but was missing", f.loc)
		return
	}
	obj, ok := f.parameters.(templateObject)
	if !ok {
		// Refused or invalid, and already reported.
		return
	}
	named := false
	for _, m := range obj.members {
		switch m.key {
		case "FunctionName":
			named = true
		case "InvocationType":
			lit, isLiteral := m.value.(templateLiteral)
			if s, isString := lit.value.(document.String); isLiteral && (!isString || s != "RequestResponse") {
				p.unsupported("lambda:invoke with InvocationType "+text(lit.value)+"; only RequestResponse is", at)
			}
		}
	}
	if !named {
		p.schema("The field 'FunctionName' is required but was missing", at)
	}
}

func (p *parser) buildMap(f *draft, io paths, resultPath target) state {
	switch f.count("ItemProcessor", "Iterator") {
	case 0:
		p.schema("These fields are required: [ItemProcessor]", f.loc)
	case 2:
		p.exclusive(f, "ItemProcessor", "Iterator")
	}
	p.exclusive(f, "ItemSelector", "Parameters")
	p.exclusive(f, "MaxConcurrency", "MaxConcurrencyPath")
	s := mapState{
		name: f.name, next: f.next, io: io, itemsPath: f.itemsPath, itemSelector: f.itemSelector,
		resultSelector: f.resultSelector, resultPath: resultPath, processor: f.processor, concurrency: f.limit,
		retry: f.retry, catch: f.catch,
	}
	if s.itemSelector == nil {
		s.itemSelector = f.parameters
	}
	if !f.has("ItemsPath") {
		s.itemsPath = rootPath()
	}
	if s.concurrency == nil {
		s.concurrency = maxConcurrency{}
	}
	return s
}

// rootPath is "$".
func rootPath() jsonpath.Path {
	path, err := jsonpath.Parse("$", jsonpath.Reference)
	if err != nil {
		// "$" is a Reference Path by the grammar.
		return jsonpath.Path{}
	}
	return path
}

// branches reads a Parallel's Branches; each branch is a scope of its own
// (stepfunctions-asl.md 3.2, 3.3).
func (p *parser) branches(v document.Value, at string) []string {
	list, ok := v.(document.List)
	if !ok {
		p.schema("The field 'Branches' must be an array", at)
		return nil
	}
	if len(list) == 0 {
		// Recorded (stepfunctions-asl.md 3.3).
		p.schema("Value cannot be empty", at)
		return nil
	}
	out := make([]string, 0, len(list))
	for i, b := range list {
		if start, ok := p.subScope(b, indexed(at, i), nil); ok {
			out = append(out, start)
		}
	}
	return out
}

// processor reads a Map's ItemProcessor or Iterator.
func (p *parser) processor(v document.Value, field, at string) string {
	var extra []string
	if field == "ItemProcessor" {
		extra = []string{"ProcessorConfig"}
	}
	obj, ok := v.(document.Map)
	if ok && field == "ItemProcessor" {
		if cfg, has := member(obj, "ProcessorConfig"); has {
			p.processorConfig(cfg, at+"/ProcessorConfig")
		}
	}
	start, _ := p.subScope(v, at, extra)
	return start
}

// processorConfig accepts Mode INLINE only; DISTRIBUTED and ExecutionType
// are Distributed Map (stepfunctions-asl.md 9.3).
func (p *parser) processorConfig(v document.Value, at string) {
	obj, ok := v.(document.Map)
	if !ok {
		p.schema("The field 'ProcessorConfig' must be an object", at)
		return
	}
	for _, e := range obj {
		switch e.Key {
		case "Mode":
			s, isString := e.Value.(document.String)
			switch {
			case isString && s == "INLINE":
			case isString && s == "DISTRIBUTED":
				p.unsupported("Distributed Map (ProcessorConfig Mode DISTRIBUTED)", at+"/Mode")
			default:
				p.schema("The field 'Mode' should have one of these values: [INLINE, DISTRIBUTED]", at+"/Mode")
			}
		case "ExecutionType":
			p.unsupported("Distributed Map (ProcessorConfig ExecutionType)", at+"/ExecutionType")
		default:
			p.unknownField(e.Key, at)
		}
	}
}

// subScope reads a branch or processor object at loc: StartAt, States,
// Comment and the extra fields the caller reads itself.
func (p *parser) subScope(v document.Value, loc string, extra []string) (string, bool) {
	obj, ok := v.(document.Map)
	if !ok {
		p.schema("A branch must be an object", loc)
		return "", false
	}
	if !p.required(obj, loc) {
		return "", false
	}
	for _, e := range obj {
		switch {
		case e.Key == "StartAt" || e.Key == "States" || slices.Contains(extra, e.Key):
		case e.Key == "Comment":
			p.str(e.Value, e.Key, loc+"/Comment")
		default:
			p.unknownField(e.Key, loc)
		}
	}
	return p.scope(obj, loc), true
}
