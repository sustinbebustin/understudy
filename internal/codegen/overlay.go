package codegen

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Overlay is one parsed codegen/overlays file (ADR 0002): apply entries that
// add traits to model shapes and members, and new shapes.
type Overlay struct {
	Source  string
	entries []overlayEntry
}

type overlayEntry struct {
	key     string
	target  smithy.ShapeID
	member  string // set for Shape$member apply entries
	replace bool
	traits  Traits
	shape   *Shape // set for a new shape; nil for an apply entry
}

// ParseOverlay reads an overlay. Every entry must carry
// dev.understudy#evidence, and every dev.understudy trait must be one of the
// closed set.
func ParseOverlay(r io.Reader, source string) (*Overlay, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, failf(StageOverlay, source, "read: %w", err)
	}
	var top struct {
		Smithy string    `json:"smithy"`
		Shapes rawShapes `json:"shapes"`
	}
	if err := strictUnmarshal(raw, &top); err != nil {
		if dup := (*duplicateKeyError)(nil); errors.As(err, &dup) {
			return nil, failf(StageOverlay, source, "%w", err)
		}
		return nil, failf(StageOverlay, source, "not an overlay (a Smithy JSON AST fragment with \"smithy\" and \"shapes\"): %w", err)
	}
	if top.Smithy != "2.0" {
		return nil, failf(StageOverlay, source, `"smithy" is %q; overlays are Smithy 2.0 fragments`, top.Smithy)
	}
	o := &Overlay{Source: source}
	for _, key := range slices.Sorted(maps.Keys(top.Shapes)) {
		e, err := parseEntry(key, top.Shapes[key], source)
		if err != nil {
			return nil, err
		}
		o.entries = append(o.entries, e)
	}
	return o, nil
}

func parseEntry(key string, raw json.RawMessage, source string) (overlayEntry, error) {
	subject := source + ": " + key
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return overlayEntry{}, failf(StageOverlay, subject, "%w", err)
	}
	var e overlayEntry
	var err error
	if head.Type == "apply" {
		e, err = parseApply(key, raw, subject)
	} else {
		e, err = parseNewShape(key, raw, source, subject)
	}
	if err != nil {
		return e, err
	}
	if len(e.traits) == 0 {
		return e, failf(StageOverlay, subject, "the entry applies no traits")
	}
	if err := checkOverlayTraits(subject, e); err != nil {
		return e, err
	}
	return e, nil
}

// parseApply reads an apply entry keyed namespace#Shape or
// namespace#Shape$member.
func parseApply(key string, raw json.RawMessage, subject string) (overlayEntry, error) {
	shapeKey, member, isMember := strings.Cut(key, "$")
	e := overlayEntry{key: key, member: member}
	var a struct {
		Type    string `json:"type"`
		Traits  Traits `json:"traits"`
		Replace bool   `json:"replace"`
	}
	if err := strictUnmarshal(raw, &a); err != nil {
		if dup := (*duplicateKeyError)(nil); errors.As(err, &dup) {
			return e, failf(StageOverlay, subject, "%w", err)
		}
		return e, failf(StageOverlay, subject, "an apply entry has only type, traits and replace: %w", err)
	}
	id, err := smithy.Parse(shapeKey)
	if err != nil || (isMember && member == "") {
		return e, failf(StageOverlay, subject, "an apply key is namespace#Shape or namespace#Shape$member")
	}
	e.target, e.traits, e.replace = id, a.Traits, a.Replace
	return e, nil
}

// parseNewShape reads a shape the overlay defines, such as an unmodeled
// error structure or an internal operation.
func parseNewShape(key string, raw json.RawMessage, source, subject string) (overlayEntry, error) {
	if strings.Contains(key, "$") {
		return overlayEntry{key: key}, failf(StageOverlay, subject, "a member key must be an apply entry")
	}
	s, err := parseShape(key, raw, source, StageOverlay)
	if err != nil {
		return overlayEntry{key: key}, err
	}
	return overlayEntry{key: key, target: s.ID, traits: s.Traits, shape: s}, nil
}

// checkOverlayTraits enforces mandatory evidence and the closed
// dev.understudy set on an entry and on the members of a new shape.
func checkOverlayTraits(subject string, e overlayEntry) error {
	ev, ok := e.traits[utEvidence]
	if !ok {
		return failf(StageOverlay, subject, "has no %s; every overlay entry states its evidence {label, source, validated?}", utEvidence)
	}
	items, err := parseEvidence(ev)
	if err != nil {
		return failf(StageOverlay, subject, "%s: %w", utEvidence, err)
	}
	if err := checkCoverage(items, e.traits); err != nil {
		return failf(StageOverlay, subject, "%s: %w", utEvidence, err)
	}
	all := []Traits{e.traits}
	if e.shape != nil {
		for _, m := range e.shape.Members {
			all = append(all, m.Traits)
		}
	}
	for _, ts := range all {
		for _, id := range ts.IDs() {
			if !strings.HasPrefix(id, understudyNamespace) {
				continue
			}
			if _, known := understudyTraits()[id]; !known {
				return failf(StageOverlay, subject, "%s is not a dev.understudy trait; the closed set is %v", id, slices.Sorted(maps.Keys(understudyTraits())))
			}
		}
	}
	if e.shape == nil && e.traits.Has(utInternalOperation) {
		return failf(StageOverlay, subject, "%s marks an operation the overlay defines, not one the model has", utInternalOperation)
	}
	if e.shape != nil && e.shape.Kind == KindOperation && !e.traits.Has(utInternalOperation) {
		return failf(StageOverlay, subject, "defines an operation without %s; no service lists an overlay operation, so it would never be generated. Mark it internal, or leave modeled operations to the model", utInternalOperation)
	}
	return nil
}

// Merge applies o to m. A new shape must not exist yet; an apply entry's
// target must. A trait the target already has fails as a duplicate, or as a
// conflict when the values differ, unless the entry says "replace": true,
// so a model bump that fixes a gap surfaces as a failing generate. A member
// apply is also checked against the Smithy traits of the member's target
// shape, since a member trait overrides the target's in the effective
// traits: a bump that constrains the target, or retargets the member to a
// constrained shape, must fail the same way.
func (m *Model) Merge(o *Overlay) error {
	for _, e := range o.entries {
		subject := o.Source + ": " + e.key
		if e.shape != nil {
			if prev, exists := m.Shapes[e.target]; exists {
				return failf(StageOverlay, subject, "redefines a shape from %s; use an apply entry to add traits to it", prev.Source)
			}
			m.Shapes[e.target] = e.shape
			continue
		}
		target, ok := m.Shapes[e.target]
		if !ok {
			return failf(StageOverlay, subject, "applies to %s, which is not in the model", e.target)
		}
		traits := target.Traits
		var inherited *traitSource
		if e.member != "" {
			mem, ok := target.Member(e.member)
			if !ok {
				return failf(StageOverlay, subject, "%s has no member %q", e.target, e.member)
			}
			traits = mem.Traits
			if t, ok := m.Shapes[mem.Target]; ok {
				inherited = &traitSource{where: "the member's target " + string(mem.Target) + " carries", traits: t.Traits}
			}
		}
		if err := applyTraits(subject, traits, inherited, e); err != nil {
			return err
		}
	}
	return nil
}

// traitSource is a trait set an apply entry is checked against, and the
// phrase that names it in a duplicate or conflict message.
type traitSource struct {
	where  string
	traits Traits
}

// applyTraits merges an apply entry's traits into dst. Its evidence stays
// with the entry: several entries may apply to one shape, each backed by
// its own evidence. A Smithy trait is also checked against inherited, the
// member target's traits when dst is a member's, but only dst changes.
func applyTraits(subject string, dst Traits, inherited *traitSource, e overlayEntry) error {
	replaced := false
	for _, id := range e.traits.IDs() {
		if id == utEvidence {
			continue
		}
		value := e.traits[id]
		sources := []traitSource{{where: "already present", traits: dst}}
		if inherited != nil && !strings.HasPrefix(id, understudyNamespace) {
			sources = append(sources, *inherited)
		}
		for _, src := range sources {
			prev, exists := src.traits[id]
			if !exists {
				continue
			}
			a, errA := canonicalJSON(prev)
			b, errB := canonicalJSON(value)
			switch {
			case errA != nil || errB != nil:
				return failf(StageOverlay, subject, "%s: the value is not JSON", id)
			case a == b:
				return failf(StageOverlay, subject, "%s duplicates the value %s (%s); delete the overlay entry, the model now carries it", id, src.where, a)
			case !e.replace:
				return failf(StageOverlay, subject, "%s conflicts with the value %s: model %s, overlay %s; set \"replace\": true if the overlay must win", id, src.where, a, b)
			}
			replaced = true
		}
		dst[id] = value
	}
	if e.replace && !replaced {
		return failf(StageOverlay, subject, `"replace" is set but no trait of the entry replaces one already present; drop "replace"`)
	}
	return nil
}

// checkUnderstudy validates the site and value of every dev.understudy trait
// once all overlays are merged, since a site such as "error structure"
// depends on traits other entries apply.
func (m *Model) checkUnderstudy() error {
	for _, id := range slices.Sorted(maps.Keys(m.Shapes)) {
		s := m.Shapes[id]
		if err := checkUnderstudySite(string(id), s.Traits, shapeSite(s)); err != nil {
			return err
		}
		for _, mem := range s.Members {
			if err := checkUnderstudySite(string(id)+"$"+mem.Name, mem.Traits, siteMember); err != nil {
				return err
			}
		}
	}
	return nil
}

func shapeSite(s *Shape) site {
	out := siteAnyShape
	switch s.Kind {
	case KindService:
		out |= siteService
	case KindOperation:
		out |= siteOperation
	case KindResource:
	case KindData:
		if s.Type == smithy.TypeStructure && s.Traits.Has(traitError) {
			out |= siteErrorShape
		}
		if s.Type == smithy.TypeString {
			out |= siteStringShape
		}
	}
	return out
}

func checkUnderstudySite(subject string, traits Traits, at site) error {
	for _, id := range traits.IDs() {
		if !strings.HasPrefix(id, understudyNamespace) {
			continue
		}
		spec, ok := understudyTraits()[id]
		if !ok {
			return failf(StageOverlay, subject, "%s is not a dev.understudy trait", id)
		}
		if spec.sites&at == 0 {
			return failf(StageOverlay, subject, "%s belongs on %s", id, spec.sites)
		}
		if err := spec.parse(traits[id]); err != nil {
			return failf(StageOverlay, subject, "%s: %w", id, err)
		}
	}
	return nil
}
