package coverage

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// Overlays is the overlay evidence of a report: the entries read from
// codegen/overlays, or none, because a running server does not carry the
// overlay files.
//
//sumtype:decl
type Overlays interface{ isOverlays() }

// OverlaysRead are the evidence items of every overlay entry.
type OverlaysRead struct {
	Entries []OverlayEntry `json:"entries"`
}

// OverlaysNotCarried is a report built without the overlay files: the
// binary does not embed them, so docs/coverage.md, generated in the
// repository, is where overlay evidence is listed.
type OverlaysNotCarried struct{}

func (OverlaysRead) isOverlays()       {}
func (OverlaysNotCarried) isOverlays() {}

// OverlayEntry is one evidence item of an overlay entry: an entry states
// one item for all its traits, or one per group of traits it covers.
type OverlayEntry struct {
	// File is the overlay file's name, such as "sqs.json".
	File string `json:"file"`
	// Shape is the shape the entry adds or applies to.
	Shape string         `json:"shape"`
	Label evidence.Label `json:"label"`
	// Source is the link or research section the label rests on.
	Source string `json:"source"`
	// Validated is the date the source was checked, when the entry gives
	// one.
	Validated string `json:"validated,omitempty"`
	// Covers are the traits the item backs; empty means all of them.
	Covers []string `json:"covers,omitempty"`
}

// OverlayError reports an overlay file coverage cannot read.
type OverlayError struct {
	File   string
	Shape  string
	Reason string
}

func (e *OverlayError) Error() string {
	if e.Shape == "" {
		return fmt.Sprintf("codegen/overlays/%s: %s", e.File, e.Reason)
	}
	return fmt.Sprintf("codegen/overlays/%s: %s: %s", e.File, e.Shape, e.Reason)
}

const evidenceTrait = "dev.understudy#evidence"

// ParseOverlay reads the evidence of every entry of the overlay file
// named file, in file order. The generator refuses an entry without
// evidence, so one here is an error too.
func ParseOverlay(file string, data []byte) ([]OverlayEntry, error) {
	d := jsonx.NewDecoder(data)
	var out []OverlayEntry
	err := members(d, func(name string) error {
		if name != "shapes" {
			return d.Skip()
		}
		return members(d, func(shape string) error {
			items, err := shapeEvidence(d)
			if err != nil {
				return &OverlayError{File: file, Shape: shape, Reason: err.Error()}
			}
			if len(items) == 0 {
				return &OverlayError{File: file, Shape: shape, Reason: "the entry states no " + evidenceTrait}
			}
			for _, it := range items {
				it.File, it.Shape = file, shape
				out = append(out, it)
			}
			return nil
		})
	})
	if err == nil {
		err = d.End()
	}
	var named *OverlayError
	switch {
	case errors.As(err, &named):
		return nil, err
	case err != nil:
		return nil, &OverlayError{File: file, Reason: err.Error()}
	}
	return out, nil
}

// shapeEvidence reads one shape entry and returns its evidence items.
func shapeEvidence(d *jsonx.Decoder) ([]OverlayEntry, error) {
	var items []OverlayEntry
	err := members(d, func(name string) error {
		if name != "traits" {
			return d.Skip()
		}
		return members(d, func(trait string) error {
			if trait != evidenceTrait {
				return d.Skip()
			}
			var err error
			items, err = evidenceItems(d)
			return err
		})
	})
	return items, err
}

// evidenceItems reads the trait's value: one item, or a list of them.
func evidenceItems(d *jsonx.Decoder) ([]OverlayEntry, error) {
	k, err := d.Peek()
	if err != nil {
		return nil, err
	}
	if k == jsonx.Object {
		it, err := evidenceItem(d)
		return []OverlayEntry{it}, err
	}
	if err := d.BeginArray(); err != nil {
		return nil, fmt.Errorf("%s is neither an evidence item nor a list of them: %w", evidenceTrait, err)
	}
	var out []OverlayEntry
	for {
		more, err := d.NextElement()
		if err != nil || !more {
			return out, err
		}
		it, err := evidenceItem(d)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
}

// evidenceItem reads {label, source, validated?, covers?}.
func evidenceItem(d *jsonx.Decoder) (OverlayEntry, error) {
	var it OverlayEntry
	var label string
	err := members(d, func(name string) error {
		var err error
		switch name {
		case "label":
			label, err = d.ReadString()
		case "source":
			it.Source, err = d.ReadString()
		case "validated":
			it.Validated, err = d.ReadString()
		case "covers":
			it.Covers, err = readStrings(d)
		default:
			err = fmt.Errorf("an evidence item has no member %q; it holds label, source, validated and covers", name)
		}
		return err
	})
	if err != nil {
		return OverlayEntry{}, err
	}
	if it.Label, err = evidence.ParseLabel(label); err != nil {
		return OverlayEntry{}, err
	}
	if it.Source == "" {
		return OverlayEntry{}, fmt.Errorf("an evidence item labeled %s names no source", label)
	}
	return it, nil
}

// members calls f with each member name of the object at d, f consuming
// the member's value.
func members(d *jsonx.Decoder, f func(name string) error) error {
	if err := d.BeginObject(); err != nil {
		return err
	}
	for {
		name, ok, err := d.NextName()
		if err != nil || !ok {
			return err
		}
		if err := f(name); err != nil {
			return err
		}
	}
}

func readStrings(d *jsonx.Decoder) ([]string, error) {
	if err := d.BeginArray(); err != nil {
		return nil, err
	}
	var out []string
	for {
		more, err := d.NextElement()
		if err != nil || !more {
			return out, err
		}
		s, err := d.ReadString()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
}

// SortOverlayEntries orders entries by label (strongest evidence first),
// then file and shape.
func SortOverlayEntries(entries []OverlayEntry) {
	slices.SortStableFunc(entries, func(a, b OverlayEntry) int {
		return cmp.Or(cmp.Compare(labelRank(a.Label), labelRank(b.Label)), cmp.Compare(a.File, b.File), cmp.Compare(a.Shape, b.Shape))
	})
}
