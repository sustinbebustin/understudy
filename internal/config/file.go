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
	BaseDomains    []string `json:"baseDomains"`
	CrossRegion    *string  `json:"crossRegion"`
	MaxObjectBytes *int64   `json:"maxObjectBytes"`
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

type fileIAM struct {
	AssumeRole *string `json:"assumeRole"`
}

type fileDynamoDB struct {
	Enabled     *bool   `json:"enabled"`
	Registry    *string `json:"registry"`
	MemoryLimit *int64  `json:"memoryLimit"`
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
	for _, s := range Settings() {
		scalarKeys = append(scalarKeys, s.Key)
	}
	sectionType := reflect.TypeFor[fileSections]()
	sectionKeys := jsonKeys(sectionType)
	valid := append(slices.Clone(scalarKeys), sectionKeys...)
	for _, key := range slices.Sorted(maps.Keys(top)) {
		where := name + ": " + key
		switch {
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
			return fileDoc{}, &Error{Where: where, Reason: "is not a configuration key", Closest: Closest(key, valid), Hint: "fix or remove the key"}
		}
	}
	if err := json.Unmarshal(data, &doc.sections); err != nil {
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return fileDoc{}, &Error{Where: name + ": " + typeErr.Field, Reason: "is a JSON " + typeErr.Value + "; expected " + typeName(typeErr.Type)}
		}
		return fileDoc{}, &Error{Where: name, Reason: err.Error()}
	}
	return doc, nil
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
