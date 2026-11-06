package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// reservedLabelPrefix starts every label key understudy sets itself
// (docker.ReservedPrefix, which config does not import). dockerLabels may
// not use it, so added labels never change what a sweep selects
// (testcontainers.md 9.3).
const reservedLabelPrefix = "understudy.dev/"

// labelsExample is the label object the refusals show.
const labelsExample = `{"com.example.team": "payments"}`

// setDockerLabels reads dockerLabels: the text of a JSON object of label
// keys to string values. Keys are checked in sorted order and the first
// failure is refused: an empty key, a key under understudy.dev/, then a
// value that is not a string. A key given twice keeps its last value, as
// encoding/json decodes an object.
func (d *draft) setDockerLabels(v value) *Error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(v.text), &raw); err != nil || raw == nil {
		return &Error{Where: v.where, Reason: strconv.Quote(v.text) + " is not a JSON object of label keys to string values", Hint: "write " + labelsExample}
	}
	labels := make(map[string]string, len(raw))
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		switch {
		case k == "":
			return &Error{Where: v.where, Reason: "has an empty label key", Hint: "name every label, as in " + labelsExample}
		case strings.HasPrefix(k, reservedLabelPrefix):
			return &Error{Where: v.where, Reason: fmt.Sprintf("label key %q is reserved for understudy's own labels", k), Hint: "choose a key outside " + reservedLabelPrefix}
		}
		// A JSON null decodes as a nil pointer, so only a string sets s.
		var s *string
		if err := json.Unmarshal(raw[k], &s); err != nil || s == nil {
			return &Error{Where: v.where, Reason: fmt.Sprintf("label %q has a value that is not a string", k), Hint: fmt.Sprintf(`quote it, as in {%q: "1"}`, k)}
		}
		labels[k] = *s
	}
	d.cfg.DockerLabels = labels
	return nil
}

// jsonText is the text a flag would carry for a file setting whose value
// is JSON (dockerLabels): the content of a JSON string, else the value as
// written, which the setting's parser refuses unless it is the JSON it
// takes.
func jsonText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
