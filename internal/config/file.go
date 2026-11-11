package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// fileDoc is the parsed configuration file: its scalar settings as text,
// and its sections.
type fileDoc struct {
	name     string
	scalars  map[string]value
	sections fileSections
}

// fileSections are the structured settings only the file holds. Pointers
// and nil slices are absent keys.
type fileSections struct {
	Accounts *fileAccounts     `json:"accounts"`
	Regions  []string          `json:"regions"`
	S3       *fileS3           `json:"s3"`
	Lambda   *fileLambda       `json:"lambda"`
	SQS      *fileSQS          `json:"sqs"`
	SNS      *fileSNS          `json:"sns"`
	SES      *fileSES          `json:"ses"`
	IAM      *fileIAM          `json:"iam"`
	DynamoDB *fileDynamoDB     `json:"dynamodb"`
	Journal  *fileJournal      `json:"journal"`
	Faults   []json.RawMessage `json:"faults"`
}

type fileAccounts struct {
	Default *string   `json:"default"`
	Keys    []fileKey `json:"keys"`
}

type fileKey struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	Account         string `json:"account"`
}

type fileS3 struct {
	BaseDomains []string `json:"baseDomains"`
	CrossRegion *string  `json:"crossRegion"`
}

type fileLambda struct {
	Network          *string         `json:"network"`
	BridgeGateway    *string         `json:"bridgeGateway"`
	StartConcurrency *int            `json:"startConcurrency"`
	IdleTimeout      *string         `json:"idleTimeout"`
	MaxEnvironments  *int            `json:"maxEnvironments"`
	StateTransitions *string         `json:"stateTransitions"`
	HotReload        []fileHotReload `json:"hotReload"`
}

type fileHotReload struct {
	Function string `json:"function"`
	Dir      string `json:"dir"`
	Account  string `json:"account"`
	Region   string `json:"region"`
}

type fileSQS struct {
	QueueDeletedRecently *string `json:"queueDeletedRecently"`
}

// fileSNS holds only scalar settings, read as their flags would be.
type fileSNS struct {
	HTTPDelivery *fileSNSHTTPDelivery `json:"httpDelivery"`
}

type fileSNSHTTPDelivery struct {
	Allow    []string `json:"allow"`
	CABundle *string  `json:"caBundle"`
}

type fileSES struct {
	VerifiedIdentities []string `json:"verifiedIdentities"`
	ProductionAccess   *bool    `json:"productionAccess"`
}

// fileIAM holds the scalar settings iam.assumeRole and iam.enforce, each
// read as its flag would be.
type fileIAM struct {
	AssumeRole *string `json:"assumeRole"`
	Enforce    *string `json:"enforce"`
}

type fileDynamoDB struct {
	Enabled     *bool  `json:"enabled"`
	MemoryLimit *int64 `json:"memoryLimit"`
}

type fileJournal struct {
	Capacity *int    `json:"capacity"`
	MaxBytes *int    `json:"maxBytes"`
	File     *string `json:"file"`
}

// parseFile reads the configuration file strictly: an unknown key at any
// depth is an *Error naming it and the closest valid key there.
func parseFile(name string, data []byte) (fileDoc, *Error) {
	doc := fileDoc{name: name, scalars: map[string]value{}}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return fileDoc{}, &Error{Where: name, Reason: "is not a JSON object: " + describe(err), Hint: `write {"listen": "127.0.0.1:4566"} or another object of settings`}
	}
	scalarKeys := make([]string, 0, len(Settings()))
	var nestedKeys []string
	for _, s := range Settings() {
		if strings.Contains(s.Key, ".") {
			nestedKeys = append(nestedKeys, s.Key)
			continue
		}
		scalarKeys = append(scalarKeys, s.Key)
	}
	sectionType := reflect.TypeFor[fileSections]()
	sectionKeys := jsonKeys(sectionType)
	valid := append(slices.Clone(scalarKeys), sectionKeys...)
	for _, key := range slices.Sorted(maps.Keys(top)) {
		where := name + ": " + key
		switch {
		case key == keyDockerLabels:
			// The file takes the label object itself, or a string holding
			// its text, as the flag does.
			doc.scalars[key] = value{text: jsonText(top[key]), where: where, source: fromFile}
		case slices.Contains(nestedKeys, key):
			return fileDoc{}, nestedOnly(key, where)
		case slices.Contains(scalarKeys, key):
			text, err := scalarText(top[key])
			if err != nil {
				return fileDoc{}, &Error{Where: where, Reason: err.Error()}
			}
			doc.scalars[key] = value{text: text, where: where, source: fromFile}
		case slices.Contains(sectionKeys, key):
			if err := checkKeys(top[key], fieldType(sectionType, key), where); err != nil {
				return fileDoc{}, err
			}
		default:
			candidates := valid
			if strings.Contains(key, ".") {
				// A dotted name is nearest a nested setting's key, whose
				// refusal then gives the nested form.
				candidates = append(slices.Clone(valid), nestedKeys...)
			}
			return fileDoc{}, &Error{Where: where, Reason: "is not a configuration key", Closest: Closest(key, candidates), Hint: "fix or remove the key"}
		}
	}
	if err := json.Unmarshal(data, &doc.sections); err != nil {
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return fileDoc{}, typeError(name, typeErr, nestedKeys)
		}
		return fileDoc{}, &Error{Where: name, Reason: err.Error()}
	}
	doc.liftNested()
	return doc, nil
}

// typeError refuses a value of the wrong JSON type at the path the decoder
// names, an array element written [i] as checkKeys writes it. An entry of
// a nested setting's list is named by its setting, as the flag would be,
// with the entry's index in the reason, as the setting's other refusals
// give it.
func typeError(name string, e *json.UnmarshalTypeError, nestedKeys []string) *Error {
	reason := "is a JSON " + e.Value + "; expected " + typeName(e.Type)
	for _, key := range nestedKeys {
		if index, ok := strings.CutPrefix(e.Field, key+"."); ok {
			return &Error{Where: name + ": " + key, Reason: "entry " + index + ": " + reason}
		}
	}
	var path strings.Builder
	for i, part := range strings.Split(e.Field, ".") {
		switch {
		case strings.Trim(part, "0123456789") == "" && part != "":
			path.WriteString("[" + part + "]")
		case i > 0:
			path.WriteString("." + part)
		default:
			path.WriteString(part)
		}
	}
	return &Error{Where: name + ": " + path.String(), Reason: reason}
}

// nestedOnly refuses a nested setting's dotted key at the top level of the
// file, rather than reading it as a second spelling, and shows the nested
// form the file takes.
func nestedOnly(key, where string) *Error {
	path := strings.Split(key, ".")
	nested := "..."
	for _, p := range slices.Backward(path) {
		nested = "{" + quote(p) + ": " + nested + "}"
	}
	section, member := strings.Join(path[:len(path)-1], "."), path[len(path)-1]
	return &Error{Where: where, Reason: "is not a top-level key; the setting is " + quote(member) + " in the " + quote(section) + " section", Hint: "write it as " + nested}
}

// liftNested reads the scalar settings the file holds nested in its
// sections as the file's values of their dotted keys, so they take the
// precedence and checks of every scalar setting.
func (doc *fileDoc) liftNested() {
	at := func(key string) string { return doc.name + ": " + key }
	if iam := doc.sections.IAM; iam != nil {
		for key, text := range map[string]*string{keyIAMEnforce: iam.Enforce, keyIAMAssumeRole: iam.AssumeRole} {
			if text != nil {
				doc.scalars[key] = value{text: *text, where: at(key), source: fromFile}
			}
		}
	}
	if sns := doc.sections.SNS; sns != nil && sns.HTTPDelivery != nil {
		if allow := sns.HTTPDelivery.Allow; allow != nil {
			doc.scalars[keySNSAllow] = value{list: allow, where: at(keySNSAllow), source: fromFile}
		}
		if bundle := sns.HTTPDelivery.CABundle; bundle != nil {
			doc.scalars[keySNSCABundle] = value{text: *bundle, where: at(keySNSCABundle), source: fromFile}
		}
	}
}

// scalarText is a scalar setting's value as the text a flag would carry: a
// string as is, a number as written, a boolean as true or false.
func scalarText(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch v := v.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	default:
		return "", errors.New("is not a string, number or boolean; give the value a flag would take")
	}
}

// checkKeys refuses a member of raw, at any depth, that the struct type t
// (or the struct a slice or pointer type holds) has no key for. A value of
// another JSON type is left to the decoder, which names the type it wants.
func checkKeys(raw json.RawMessage, t reflect.Type, where string) *Error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var items []json.RawMessage
	if t.Kind() == reflect.Slice && json.Unmarshal(raw, &items) == nil {
		for i, item := range items {
			if err := checkKeys(item, t.Elem(), where+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
	}
	var members map[string]json.RawMessage
	if t.Kind() == reflect.Struct && json.Unmarshal(raw, &members) == nil {
		return checkMembers(members, t, where)
	}
	return nil
}

// checkMembers refuses a member struct type t has no key for, and checks
// the members it has.
func checkMembers(members map[string]json.RawMessage, t reflect.Type, where string) *Error {
	keys := jsonKeys(t)
	for _, key := range slices.Sorted(maps.Keys(members)) {
		if !slices.Contains(keys, key) {
			return &Error{Where: where + "." + key, Reason: "is not a configuration key", Closest: Closest(key, keys), Hint: "fix or remove the key"}
		}
		if err := checkKeys(members[key], fieldType(t, key), where+"."+key); err != nil {
			return err
		}
	}
	return nil
}

// jsonKeys lists the json keys of struct type t's fields.
func jsonKeys(t reflect.Type) []string {
	keys := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		keys = append(keys, name)
	}
	return keys
}

// fieldType is the type of struct type t's field with json key key.
func fieldType(t reflect.Type, key string) reflect.Type {
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name == key {
			return f.Type
		}
	}
	return reflect.TypeFor[json.RawMessage]()
}

// typeName says what JSON a Go type takes, with the keys of an object, so
// a section of the wrong shape names the shape it needs: an array of
// {"function": ..., "dir": ...}.
func typeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	kind := t.Kind()
	switch {
	case kind == reflect.String:
		return "a string"
	case kind == reflect.Bool:
		return "a boolean"
	case kind == reflect.Int || kind == reflect.Int64:
		return "an integer"
	case kind == reflect.Struct:
		return "an object " + objectShape(t)
	case kind == reflect.Slice && t.Elem() == reflect.TypeFor[json.RawMessage]():
		// Only faults holds raw entries, parsed later as fault rules.
		return `an array of fault rules, each {"phase": ..., "match": ..., "action": ..., "trigger": ...}`
	case kind == reflect.Slice && t.Elem().Kind() == reflect.Struct:
		return "an array of " + objectShape(t.Elem())
	case kind == reflect.Slice:
		_, elem, _ := strings.Cut(typeName(t.Elem()), " ")
		return "an array of " + elem + "s"
	}
	return "an object"
}

// objectShape lists the keys of struct type t: {"function": ..., "dir": ...}.
func objectShape(t reflect.Type) string {
	keys := jsonKeys(t)
	for i, k := range keys {
		keys[i] = strconv.Quote(k) + ": ..."
	}
	return "{" + strings.Join(keys, ", ") + "}"
}

func describe(err error) string {
	if err == nil {
		return "the document is null"
	}
	return err.Error()
}
