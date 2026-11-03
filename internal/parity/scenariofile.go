package parity

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// scenarioFile is a scenario as its file spells it. Each struct's members
// are in the canonical order Encode writes (testdata/README.md, scenarios);
// only optional members are left out when empty.
type scenarioFile struct {
	Version int           `json:"version"`
	Item    string        `json:"item"`
	Account string        `json:"account"`
	Region  string        `json:"region"`
	Client  Client        `json:"client"`
	Needs   []Need        `json:"needs,omitempty"`
	Setup   []callFile    `json:"setup,omitempty"`
	Steps   []stepFile    `json:"steps"`
	Waivers []waiverFile  `json:"waivers,omitempty"`
	Settles []claimFile   `json:"settles,omitempty"`
	Record  []commandFile `json:"record,omitempty"`
}

type callFile struct {
	Service   string `json:"service"`
	Operation string `json:"operation"`
	// Input is a JSON object, written nested.
	Input  jsontext.Value  `json:"input"`
	Bind   []setupBindFile `json:"bind,omitempty"`
	Before string          `json:"before,omitempty"`
}

type stepFile struct {
	Fixture string      `json:"fixture"`
	Compare CompareMode `json:"compare"`
	Bind    []bindFile  `json:"bind,omitempty"`
	// Advance is a time.Duration as its String method writes it.
	Advance string `json:"advance,omitempty"`
	Signing string `json:"signing,omitempty"`
}

type bindFile struct {
	Path  Path           `json:"path"`
	Shape jsontext.Value `json:"shape"`
}

type setupBindFile struct {
	Path  Path           `json:"path"`
	Shape jsontext.Value `json:"shape"`
	AWS   string         `json:"aws"`
}

type waiverFile struct {
	Step   string `json:"step"`
	Field  Path   `json:"field"`
	Reason string `json:"reason"`
}

// claimFile is a settles entry: exactly one of Switch and Overlay is set,
// and Covers only beside Overlay.
type claimFile struct {
	Switch  *string  `json:"switch,omitzero"`
	Overlay *string  `json:"overlay,omitzero"`
	Covers  []string `json:"covers,omitempty"`
	Step    string   `json:"step"`
	Field   Path     `json:"field"`
}

// commandFile is a record entry: a rec command when Step is set, else a
// command run outside rec.
type commandFile struct {
	Step    *string  `json:"step,omitzero"`
	Argv    []string `json:"argv"`
	Capture string   `json:"capture,omitempty"`
}

// scenario converts f, a decoded file, to its Scenario; the value rules are
// Scenario.check's.
func (f scenarioFile) scenario() (Scenario, error) {
	s := Scenario{Version: f.Version, Item: f.Item, Account: f.Account, Region: f.Region, Client: f.Client, Needs: f.Needs}
	var err error
	if s.Setup, err = decodeEach("setup", f.Setup, callFile.call); err != nil {
		return Scenario{}, err
	}
	if s.Steps, err = decodeEach("steps", f.Steps, stepFile.step); err != nil {
		return Scenario{}, err
	}
	for _, w := range f.Waivers {
		s.Waivers = append(s.Waivers, Waiver(w))
	}
	if s.Settles, err = decodeEach("settles", f.Settles, claimFile.claim); err != nil {
		return Scenario{}, err
	}
	if s.Record, err = decodeEach("record", f.Record, commandFile.command); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

func (f callFile) call(field string) (Call, error) {
	binds, err := decodeEach(field+".bind", f.Bind, setupBindFile.bind)
	if err != nil {
		return Call{}, err
	}
	c := Call{Service: f.Service, Operation: f.Operation, Bind: binds, Before: f.Before}
	if len(f.Input) > 0 {
		held, ok := heldInput(f.Input)
		if !ok {
			return Call{}, fieldError(field+".input", "is not JSON")
		}
		c.Input = string(held)
	}
	return c, nil
}

func (f stepFile) step(field string) (Step, error) {
	binds, err := decodeEach(field+".bind", f.Bind, bindFile.bind)
	if err != nil {
		return Step{}, err
	}
	st := Step{Fixture: f.Fixture, Compare: f.Compare, Bind: binds, Signing: f.Signing}
	if f.Advance == "" {
		return st, nil
	}
	d, err := time.ParseDuration(f.Advance)
	switch {
	case err != nil:
		return Step{}, fieldError(field+".advance", fmt.Sprintf("%q is not a Go duration, such as 6s or 1m30s", f.Advance))
	case d <= 0:
		return Step{}, fieldError(field+".advance", fmt.Sprintf("%q is not positive: an advance moves the emulator clock forward; leave it out to move nothing", f.Advance))
	}
	st.Advance = d
	return st, nil
}

func (f bindFile) bind(field string) (Bind, error) {
	sh, err := decodeShape(field+".shape", f.Shape)
	if err != nil {
		return Bind{}, err
	}
	return Bind{Path: f.Path, Shape: sh}, nil
}

func (f setupBindFile) bind(field string) (SetupBind, error) {
	sh, err := decodeShape(field+".shape", f.Shape)
	if err != nil {
		return SetupBind{}, err
	}
	return SetupBind{Path: f.Path, Shape: sh, AWS: f.AWS}, nil
}

func (f claimFile) claim(field string) (Claim, error) {
	c := Claim{Step: f.Step, Field: f.Field}
	switch {
	case f.Switch != nil && f.Overlay != nil:
		return Claim{}, fieldError(field, "names both a switch and an overlay entry; a settles entry settles one")
	case f.Switch != nil && len(f.Covers) > 0:
		return Claim{}, fieldError(field+".covers", "picks one evidence item of an overlay entry; a switch has none")
	case f.Switch != nil:
		c.Target = SwitchTarget{Name: *f.Switch}
	case f.Overlay != nil:
		c.Target = OverlayTarget{Shape: *f.Overlay, Covers: f.Covers}
	}
	return c, nil
}

func (f commandFile) command(field string) (Command, error) {
	switch {
	case f.Step == nil:
		return RunCommand{Argv: f.Argv, Capture: f.Capture}, nil
	case f.Capture != "":
		return nil, fieldError(field+".capture", "belongs to a command run outside rec; a rec command's output is its step's recording")
	}
	return RecCommand{Step: *f.Step, Argv: f.Argv}, nil
}

// decodeEach converts each element of xs, the members field[i] of a file,
// with conv; the first failure stops it. No elements give nil.
func decodeEach[F, T any](field string, xs []F, conv func(F, string) (T, error)) ([]T, error) {
	if len(xs) == 0 {
		return nil, nil
	}
	out := make([]T, 0, len(xs))
	for i, x := range xs {
		t, err := conv(x, index(field, i))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// file converts s, a checked scenario, to its file form.
func (s Scenario) file() (scenarioFile, error) {
	f := scenarioFile{Version: s.Version, Item: s.Item, Account: s.Account, Region: s.Region, Client: s.Client, Needs: s.Needs}
	var err error
	if f.Setup, err = encodeEach(s.Setup, Call.file); err != nil {
		return scenarioFile{}, err
	}
	if f.Steps, err = encodeEach(s.Steps, Step.file); err != nil {
		return scenarioFile{}, err
	}
	for _, w := range s.Waivers {
		f.Waivers = append(f.Waivers, waiverFile(w))
	}
	for _, c := range s.Settles {
		f.Settles = append(f.Settles, c.file())
	}
	for _, c := range s.Record {
		f.Record = append(f.Record, commandFileOf(c))
	}
	return f, nil
}

func (c Call) file() (callFile, error) {
	binds, err := encodeEach(c.Bind, SetupBind.file)
	if err != nil {
		return callFile{}, err
	}
	return callFile{Service: c.Service, Operation: c.Operation, Input: jsontext.Value(c.Input), Bind: binds, Before: c.Before}, nil
}

func (s Step) file() (stepFile, error) {
	binds, err := encodeEach(s.Bind, Bind.file)
	if err != nil {
		return stepFile{}, err
	}
	f := stepFile{Fixture: s.Fixture, Compare: s.Compare, Bind: binds, Signing: s.Signing}
	if s.Advance != 0 {
		f.Advance = s.Advance.String()
	}
	return f, nil
}

func (b Bind) file() (bindFile, error) {
	sh, err := encodeShape(b.Shape)
	if err != nil {
		return bindFile{}, err
	}
	return bindFile{Path: b.Path, Shape: sh}, nil
}

func (b SetupBind) file() (setupBindFile, error) {
	sh, err := encodeShape(b.Shape)
	if err != nil {
		return setupBindFile{}, err
	}
	return setupBindFile{Path: b.Path, Shape: sh, AWS: b.AWS}, nil
}

func (c Claim) file() claimFile {
	f := claimFile{Step: c.Step, Field: c.Field}
	switch t := c.Target.(type) {
	case SwitchTarget:
		f.Switch = &t.Name
	case OverlayTarget:
		f.Overlay, f.Covers = &t.Shape, t.Covers
	}
	return f
}

func commandFileOf(c Command) commandFile {
	switch c := c.(type) {
	case RecCommand:
		return commandFile{Step: &c.Step, Argv: c.Argv}
	case RunCommand:
		return commandFile{Argv: c.Argv, Capture: c.Capture}
	}
	return commandFile{}
}

// encodeEach converts each element of xs with conv; the first failure
// stops it. No elements give nil.
func encodeEach[T, F any](xs []T, conv func(T) (F, error)) ([]F, error) {
	if len(xs) == 0 {
		return nil, nil
	}
	out := make([]F, 0, len(xs))
	for _, x := range xs {
		f, err := conv(x)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// heldInput is raw in the form a Call holds its input: compact, strings in
// their shortest encoding, numbers as written. ok is false when raw is not
// valid JSON.
func heldInput(raw jsontext.Value) (held jsontext.Value, ok bool) {
	held = raw.Clone()
	if err := held.Format(); err != nil {
		return nil, false
	}
	return held, true
}

// decodeError reports err, a failure to decode the member at field of a
// scenario file ("" for the whole file), as a *ScenarioError at the member
// it names.
func decodeError(field string, err error) error {
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		return fieldError(fieldOf(field, se.JSONPointer), semanticReason(se))
	}
	if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		reason := fmt.Sprintf("is not valid JSON at byte %d: %v", se.ByteOffset, se.Err)
		if errors.Is(se.Err, jsontext.ErrDuplicateName) {
			reason = "is named twice; each member appears once"
		}
		return fieldError(fieldOf(field, se.JSONPointer), reason)
	}
	return fieldError(field, err.Error())
}

func semanticReason(se *json.SemanticError) string {
	switch {
	case errors.Is(se.Err, json.ErrUnknownName):
		return "is not a member the scenario format defines"
	case se.Err != nil && len(se.JSONValue) > 0:
		return fmt.Sprintf("%s is not %s: %v", se.JSONValue, goKindName(se.GoType), se.Err)
	case se.Err != nil:
		return fmt.Sprintf("cannot be read as %s: %v", goKindName(se.GoType), se.Err)
	}
	return fmt.Sprintf("is a JSON %s; want %s", kindName(se.JSONKind), goKindName(se.GoType))
}

// fieldOf is ptr, a JSON pointer below the member at field, in the form of
// ScenarioError.Field: members joined by ".", array indexes as "[i]".
func fieldOf(field string, ptr jsontext.Pointer) string {
	var b strings.Builder
	b.WriteString(field)
	for tok := range ptr.Tokens() {
		if isIndex(tok) {
			b.WriteString("[" + tok + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(tok)
	}
	return b.String()
}

// kindName names a JSON kind for an error message.
func kindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindNull:
		return "null"
	case jsontext.KindFalse, jsontext.KindTrue:
		return "boolean"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindBeginObject, jsontext.KindEndObject:
		return "object"
	case jsontext.KindBeginArray, jsontext.KindEndArray:
		return "array"
	case jsontext.KindInvalid:
	}
	return "value"
}

// goKindName names the JSON a decoded Go type takes, for an error message.
func goKindName(t reflect.Type) string {
	if t == nil {
		return "another JSON kind"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.String {
		return "a string"
	}
	if t.Kind() == reflect.Int {
		return "an integer"
	}
	if t.Kind() == reflect.Slice {
		return "an array"
	}
	if t.Kind() == reflect.Struct {
		return "an object"
	}
	return "a " + t.Kind().String()
}
