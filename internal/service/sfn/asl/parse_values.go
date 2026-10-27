package asl

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// jsonError is the INVALID_JSON_DESCRIPTION message and location for text
// that is not JSON. An unrecognized token where the definition starts gets
// the Jackson parser's text AWS answers (test_sfn_api_validation
// .snapshot.json:L22, stepfunctions-asl.md 3.6); other malformed JSON gets
// understudy's wording (UNVERIFIED, ASL-01).
func jsonError(text string, err error) (message, location string) {
	start := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
	end := start
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
			break
		}
		end += size
	}
	token := text[start:end]
	if token == "" || token == "true" || token == "false" || token == "null" || unicode.IsDigit(rune(token[0])) {
		return "The definition is not valid JSON: " + err.Error(), ""
	}
	line := 1 + strings.Count(text[:end], "\n")
	column := utf8.RuneCountInString(text[strings.LastIndex(text[:end], "\n")+1:end]) + 1
	location = `[Source: (String)"` + text + `"; line: ` + strconv.Itoa(line) + ", column: " + strconv.Itoa(column) + "]"
	return "Unrecognized token '" + token + "': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at " + location, location
}

// integer reads an integer field in [lo, hi].
func (p *parser) integer(v document.Value, field, at string, lo, hi int64) (int64, bool) {
	n, isNumber := v.(document.Number)
	i, err := n.Int64()
	if !isNumber || err != nil || i < lo || i > hi {
		p.schema("The field '"+field+"' must be an integer from "+strconv.FormatInt(lo, 10)+" to "+strconv.FormatInt(hi, 10), at)
		return 0, false
	}
	return i, true
}

// rfc3339 is the shape of a literal Timestamp: upper-case T, and Z or a
// numeric offset (stepfunctions-asl.md 3.5, 7.1).
var rfc3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)

// timestamp reads a Wait's literal Timestamp; the message is recorded
// (stepfunctions-asl.md 3.5).
func (p *parser) timestamp(v document.Value, at string) (time.Time, bool) {
	s, isString := v.(document.String)
	if isString && rfc3339.MatchString(string(s)) {
		if t, err := time.Parse(time.RFC3339Nano, string(s)); err == nil {
			return t, true
		}
	}
	p.schema("String does not match RFC3339 timestamp", at)
	return time.Time{}, false
}

// isVariable reports whether a path-like string is a variable reference,
// $name (stepfunctions-asl.md 4.8): understudy refuses variables (ASL-18).
func isVariable(s string) bool {
	if len(s) < 2 || s[0] != '$' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[1:])
	return r == '_' || unicode.IsLetter(r)
}

// mentionsVariable reports whether an intrinsic call has a variable
// argument: a $ that starts a name, outside quoted strings.
func mentionsVariable(expr string) bool {
	quoted := false
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case c == '\\' && quoted:
			i++
		case c == '\'':
			quoted = !quoted
		case c == '$' && !quoted && (i == 0 || expr[i-1] != '$') && isVariable(expr[i:]):
			return true
		}
	}
	return false
}

const variables = "variables"

// refOptions say what a Reference Path field may hold besides a path to
// the input.
type refOptions struct {
	// noContext refuses a "$$" path (ResultPath, stepfunctions-asl.md 4.4).
	noContext bool
	// noVariable makes a variable a schema error rather than a refusal:
	// AWS itself refuses one there (ResultPath, stepfunctions-asl.md 4.4).
	noVariable bool
}

// refPath reads a field that takes a Reference Path
// (stepfunctions-asl.md 4.5).
func (p *parser) refPath(v document.Value, field, at string, o refOptions) (jsonpath.Path, bool) {
	s, ok := p.str(v, field, at)
	if !ok {
		return jsonpath.Path{}, false
	}
	if isVariable(s) && !o.noVariable {
		p.unsupported(variables+" (the reference '"+s+"' in "+field+")", at)
		return jsonpath.Path{}, false
	}
	path, err := jsonpath.Parse(s, jsonpath.Reference)
	switch {
	case err != nil:
		p.schema("The value for the field '"+field+"' must be a valid Reference Path", at)
		return jsonpath.Path{}, false
	case path.IsContext() && o.noContext:
		p.schema("The field '"+field+"' cannot refer to the context object", at)
		return jsonpath.Path{}, false
	case path.IsContext():
		return path, p.need(FeatPayloadTemplates, "context object paths ($$)", at)
	}
	return path, true
}

// plainPath reads a Path (InputPath, OutputPath, a ".$" value); invalid is
// the schema message for one that does not parse.
func (p *parser) plainPath(s, field, at, invalid string) (jsonpath.Path, bool) {
	if isVariable(s) {
		p.unsupported(variables+" (the reference '"+s+"' in "+field+")", at)
		return jsonpath.Path{}, false
	}
	path, err := jsonpath.Parse(s, jsonpath.Plain)
	var un *jsonpath.UnsupportedError
	switch {
	case errors.As(err, &un):
		p.unsupported("the JSONPath construct '"+un.Construct+"' (in "+field+")", at)
		return jsonpath.Path{}, false
	case err != nil:
		p.schema(invalid, at)
		return jsonpath.Path{}, false
	}
	if _, refErr := jsonpath.Parse(s, jsonpath.Reference); refErr != nil &&
		!p.need(FeatPathDialect, "the JSONPath constructs beyond names and indexes (in "+field+")", at) {
		return jsonpath.Path{}, false
	}
	if path.IsContext() && !p.need(FeatPayloadTemplates, "context object paths ($$)", at) {
		return jsonpath.Path{}, false
	}
	return path, true
}

// selector reads an InputPath or OutputPath: null or a Path
// (stepfunctions-asl.md 4.2).
func (p *parser) selector(v document.Value, field, at string) (selector, bool) {
	switch v := v.(type) {
	case document.Null:
		return selectNone{}, true
	case document.String:
		path, ok := p.plainPath(string(v), field, at, "The value for the field '"+field+"' must be a valid JSONPath")
		return selectPath{path: path}, ok
	case document.Bool, document.Number, document.List, document.Map:
	}
	p.schema("The field '"+field+"' must be a string or null", at)
	return nil, false
}

// target reads a ResultPath: null or a Reference Path into the input
// (stepfunctions-asl.md 4.4).
func (p *parser) target(v document.Value, at string) target {
	if _, isNull := v.(document.Null); isNull {
		return discardResult{}
	}
	path, ok := p.refPath(v, "ResultPath", at, refOptions{noContext: true, noVariable: true})
	if !ok {
		return nil
	}
	return placeAt{path: path}
}

// template reads a payload template field (stepfunctions-asl.md 4.3); at is
// the field's location, where AWS reports problems inside it (3.5).
func (p *parser) template(v document.Value, field, at string) template {
	obj, ok := v.(document.Map)
	if !ok {
		p.schema("The field '"+field+"' must be an object", at)
		return nil
	}
	return p.templateObject(obj, at)
}

func (p *parser) templateObject(obj document.Map, at string) templateObject {
	out := templateObject{}
	keys := map[string]bool{}
	for _, e := range obj {
		key := e.Key
		var value template
		switch m, isMap := e.Value.(document.Map); {
		case strings.HasSuffix(key, ".$"):
			key = strings.TrimSuffix(key, ".$")
			value = p.expression(e.Value, e.Key, at)
		case isMap:
			value = p.templateObject(m, at)
		default:
			value = templateLiteral{value: e.Value}
		}
		if keys[key] {
			// Field names must be unique after renaming (stepfunctions-asl.md
			// 4.3); the message is understudy's (ASL-01).
			p.schema("The field '"+key+"' appears more than once the '.$' suffixes are removed", at)
		}
		keys[key] = true
		if value != nil {
			out.members = append(out.members, templateMember{key: key, value: value})
		}
	}
	return out
}

// expression reads the value of a ".$" member: a Path, a "$$" path or an
// intrinsic call. The message for anything else is recorded
// (stepfunctions-asl.md 3.5).
func (p *parser) expression(v document.Value, field, at string) template {
	if !p.need(FeatPayloadTemplates, "payload template '.$' fields", at) {
		return nil
	}
	invalid := "The value for the field '" + field + "' must be a valid JSONPath or a valid intrinsic function call"
	s, isString := v.(document.String)
	switch {
	case !isString:
		p.schema(invalid, at)
	case strings.HasPrefix(string(s), "$"):
		if path, ok := p.plainPath(string(s), field, at, invalid); ok {
			return templatePath{field: field, path: path}
		}
	case strings.HasPrefix(string(s), "States."):
		if c, ok := p.call(string(s), field, at, invalid); ok {
			return templateCall{field: field, call: c}
		}
	default:
		p.schema(invalid, at)
	}
	return nil
}

// call reads an intrinsic call.
func (p *parser) call(s, field, at, invalid string) (intrinsic.Call, bool) {
	if !p.need(FeatIntrinsics, "intrinsic functions", at) {
		return intrinsic.Call{}, false
	}
	c, err := intrinsic.Parse(s)
	var un *jsonpath.UnsupportedError
	switch {
	case err == nil:
		return c, true
	case mentionsVariable(s):
		p.unsupported(variables+" (an intrinsic argument in "+field+")", at)
	case errors.As(err, &un):
		p.unsupported("the JSONPath construct '"+un.Construct+"' (in "+field+")", at)
	default:
		p.schema(invalid, at)
	}
	return intrinsic.Call{}, false
}

// stringFunctions are the intrinsics a Fail state's ErrorPath and CausePath
// may call: those returning a string (stepfunctions-asl.md 5.1).
func stringFunctions() []string {
	return []string{
		"States.Format", "States.JsonToString", "States.ArrayGetItem", "States.Base64Encode",
		"States.Base64Decode", "States.Hash", "States.UUID",
	}
}

// messagePath reads a Fail state's ErrorPath or CausePath: a Reference Path
// or a string intrinsic (stepfunctions-asl.md 7.2).
func (p *parser) messagePath(v document.Value, field, at string) message {
	if s, ok := v.(document.String); ok && strings.HasPrefix(string(s), "States.") {
		invalid := "The value for the field '" + field + "' must be a valid Reference Path or a valid intrinsic function call"
		c, ok := p.call(string(s), field, at, invalid)
		if !ok {
			return nil
		}
		if !slices.Contains(stringFunctions(), c.Function()) {
			p.schema("The field '"+field+"' takes only the intrinsic functions that return a string, not "+c.Function(), at)
			return nil
		}
		return callMessage{call: c}
	}
	path, ok := p.refPath(v, field, at, refOptions{})
	if !ok {
		return nil
	}
	return pathMessage{path: path}
}

var (
	// functionARN is a Lambda function ARN, version or alias allowed.
	functionARN = regexp.MustCompile(`^arn:aws[a-z-]*:lambda:[a-z0-9-]+:\d{12}:function:[A-Za-z0-9_-]{1,64}(:[A-Za-z0-9_$-]+)?$`)
	// invokeARN is the optimized Lambda integration.
	invokeARN = regexp.MustCompile(`^arn:aws[a-z-]*:states:::lambda:invoke$`)
	// dynamoARN is an optimized DynamoDB integration.
	dynamoARN = regexp.MustCompile(`^arn:aws[a-z-]*:states:::dynamodb:(.*)$`)
)

// resource reads a Task's Resource. Lambda functions and lambda:invoke are
// accepted; a DynamoDB action AWS does not have gets AWS's recorded text
// (stepfunctions-asl.md 3.5), as does a value that is no ARN; every other
// ARN is refused (spec "Definition grammar"). function is the ARN of a
// FunctionARN resource.
func (p *parser) resource(v document.Value, at string) (kind ResourceKind, function string) {
	s, ok := p.str(v, "Resource", at)
	if !ok {
		return 0, ""
	}
	notRecognized := "The resource provided " + s + " is not recognized. The value is not a valid resource ARN, or the resource is not available in this region."
	if m := dynamoARN.FindStringSubmatch(s); m != nil {
		switch m[1] {
		case "getItem", "putItem", "updateItem", "deleteItem":
			p.unsupported("the DynamoDB integration "+s+"; Task resources are Lambda functions and arn:aws:states:::lambda:invoke only", at)
		default:
			p.schema(notRecognized, at)
		}
		return 0, ""
	}
	switch {
	case functionARN.MatchString(s):
		return FunctionARN, s
	case invokeARN.MatchString(s):
		return LambdaInvoke, ""
	case strings.HasPrefix(s, "arn:"):
		p.unsupported("the Task resource "+s+"; Task resources are Lambda functions and arn:aws:states:::lambda:invoke only", at)
	default:
		p.schema(notRecognized, at)
	}
	return 0, ""
}
