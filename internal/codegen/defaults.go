package codegen

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Family defaults: overlays/common.json defines one service shape per
// protocol family carrying the family's unknownOperationError and
// malformedRequestError (error-matrix.md 3, 7), and listing the family's
// generic vocabulary under errors (error-matrix.md 3, H6). A service whose
// own overlay leaves an identity unset takes the default of each family it
// speaks, so every generated package answers unknown operations and
// undecodable bodies in its family's words without restating them twelve
// times. The Query family has no malformedRequestError: no recording shows
// what a Query service sends for a form value that does not fit its member,
// and the awsquery codec answers with its own fallback.
const (
	defaultsJSON  smithy.ShapeID = "dev.understudy.defaults#JSONFamily"
	defaultsQuery smithy.ShapeID = "dev.understudy.defaults#QueryFamily"
)

// familyDefault pairs a protocol family with its defaults shape.
type familyDefault struct {
	family awserr.Family
	shape  smithy.ShapeID
}

// familyDefaults lists every family's defaults shape, in the order
// generated code lists the families.
func familyDefaults() []familyDefault {
	return []familyDefault{{awserr.JSONFamily, defaultsJSON}, {awserr.QueryFamily, defaultsQuery}}
}

// serviceFamilies reports which protocol families the service speaks: its
// model protocols plus the extras codegen/services adds.
func serviceFamilies(svc *Shape, cfg ServiceConfig) map[awserr.Family]bool {
	families := map[awserr.Family]bool{}
	for _, p := range slices.Concat(modelProtocols(svc), cfg.ExtraProtocols) {
		if f, ok := awserr.FamilyOf(p); ok {
			families[f] = true
		}
	}
	return families
}

// familyVocabulary is one family's generic vocabulary: the error shapes its
// defaults shape lists, in that order.
type familyVocabulary struct {
	family awserr.Family
	from   smithy.ShapeID // the defaults shape
	errors []smithy.ShapeID
}

// familyVocabularies lists the vocabulary of each family the service
// speaks. Overlays without the defaults shapes (the golden fixture) give
// none.
func (m *Model) familyVocabularies(svc *Shape, cfg ServiceConfig) []familyVocabulary {
	families := serviceFamilies(svc, cfg)
	var out []familyVocabulary
	for _, d := range familyDefaults() {
		def, ok := m.Shapes[d.shape]
		if !families[d.family] || !ok || len(def.Errors) == 0 {
			continue
		}
		out = append(out, familyVocabulary{family: d.family, from: def.ID, errors: def.Errors})
	}
	return out
}

// applyFamilyDefaults fills the target service's unset unknownOperationError
// families and malformedRequestError from the defaults of the families its
// protocols belong to. Overlays without the defaults shapes (the golden
// fixture) leave the service as it is.
func (m *Model) applyFamilyDefaults(cfg ServiceConfig) error {
	svc, ok := m.Shapes[cfg.Service]
	if !ok || svc.Kind != KindService {
		return failf(StageClosure, string(cfg.Service), "is not a service shape in the model")
	}
	families := serviceFamilies(svc, cfg)
	unknown := UnknownOperation{}
	if raw, ok := svc.Traits[utUnknownOperation]; ok {
		var err error
		if unknown, err = parseUnknownOperation(raw); err != nil {
			return failf(StageOverlay, string(svc.ID), "%s: %w", utUnknownOperation, err)
		}
	}
	for _, d := range familyDefaults() {
		def, ok := m.Shapes[d.shape]
		if !families[d.family] || !ok {
			continue
		}
		if err := unknown.inherit(d.family, def); err != nil {
			return err
		}
		if raw, ok := def.Traits[utMalformedRequest]; ok && !svc.Traits.Has(utMalformedRequest) {
			svc.Traits[utMalformedRequest] = raw
		}
	}
	if unknown.JSON == "" && unknown.Query == "" {
		return nil
	}
	raw, err := json.Marshal(struct {
		JSON  smithy.ShapeID `json:"json,omitempty"`
		Query smithy.ShapeID `json:"query,omitempty"`
	}{unknown.JSON, unknown.Query})
	if err != nil {
		return failf(StageOverlay, string(svc.ID), "%s: %w", utUnknownOperation, err)
	}
	svc.Traits[utUnknownOperation] = raw
	return nil
}

// inherit fills family f's identity of u from the defaults shape def when
// u leaves it unset.
func (u *UnknownOperation) inherit(f awserr.Family, def *Shape) error {
	raw, ok := def.Traits[utUnknownOperation]
	if !ok {
		return nil
	}
	d, err := parseUnknownOperation(raw)
	if err != nil {
		return failf(StageOverlay, string(def.ID), "%s: %w", utUnknownOperation, err)
	}
	switch f {
	case awserr.JSONFamily:
		u.JSON = cmp.Or(u.JSON, d.JSON)
	case awserr.QueryFamily:
		u.Query = cmp.Or(u.Query, d.Query)
	}
	return nil
}

// modelProtocols lists the protocols a service shape's traits declare, in
// trait id order.
func modelProtocols(svc *Shape) []ref.ProtocolID {
	var out []ref.ProtocolID
	for _, id := range svc.Traits.IDs() {
		if p, ok := protocolTraits()[id]; ok {
			out = append(out, p)
		}
	}
	return out
}
