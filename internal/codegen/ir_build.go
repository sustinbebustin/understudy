package codegen

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// builder carries state while the IR is built.
type builder struct {
	model    *Model
	cfg      ServiceConfig
	iam      *iamTable
	clo      *closure
	ir       *ServiceIR
	shapes   map[smithy.ShapeID]*ShapeIR
	errors   map[smithy.ShapeID]*ErrorIR
	goNames  map[string]smithy.ShapeID
	enumDef  EnumPolicy
	emptyDef EmptyCollection
}

// Build turns a merged model into the IR of the service cfg names; iam is
// the Service Reference table (nil for services no IAM policy names).
func Build(m *Model, cfg ServiceConfig, iam *iamTable, h Header) (*ServiceIR, error) {
	if err := m.checkUnderstudy(); err != nil {
		return nil, err
	}
	if err := m.applyFamilyDefaults(cfg); err != nil {
		return nil, err
	}
	clo, err := m.closure(cfg)
	if err != nil {
		return nil, err
	}
	b := &builder{
		model: m, cfg: cfg, iam: iam, clo: clo,
		ir: &ServiceIR{
			ID: cfg.ID, Package: cfg.Package, ModelFile: h.ModelFile, Origin: h.Origin, Shape: cfg.Service,
			RawBinding: cfg.RawBinding,
		},
		shapes:  map[smithy.ShapeID]*ShapeIR{},
		errors:  map[smithy.ShapeID]*ErrorIR{},
		goNames: map[string]smithy.ShapeID{},
	}
	if _, corpus := cfg.Destination.(CorpusPackage); corpus || cfg.RawBinding {
		b.ir.SerdeBoth = true
	}
	// The passes run in this order, each reading only what earlier ones
	// built: shapes before the operations that rename them, members before
	// roles, roles and error identities before the validation rules that
	// resolve against them, and rules before the patterns they may drop.
	for _, step := range []func() error{
		b.classify,
		b.service,
		b.dataShapes,
		b.operations,
		b.members,
		b.checksums,
		b.roles,
		b.errorIdentities,
		b.policies,
		b.patterns,
		b.iamActions,
		b.routes,
		b.notConfigured,
		b.cases,
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.ir, nil
}

// classify fails on any trait the table does not know.
func (b *builder) classify() error {
	check := func(subject string, traits Traits) error {
		for _, id := range traits.IDs() {
			if _, ok := Classify(id); !ok {
				return failf(StageClassify, subject, "trait %s is unclassified; add it to traitClasses in internal/codegen/trait.go as honor, generation-only or ignore (smithy-models.md 4.3)", id)
			}
		}
		return nil
	}
	all := slices.Concat([]*Shape{b.clo.service}, b.clo.operations)
	for _, id := range slices.Sorted(maps.Keys(b.clo.shapes)) {
		all = append(all, b.clo.shapes[id])
	}
	for _, s := range all {
		if err := check(string(s.ID), s.Traits); err != nil {
			return err
		}
		for _, mem := range s.Members {
			if err := check(string(s.ID)+"$"+mem.Name, mem.Traits); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) service() error {
	svc := b.clo.service
	for _, step := range []func(*Shape) error{b.serviceIdentity, b.serviceProtocols, b.serviceDefaults} {
		if err := step(svc); err != nil {
			return err
		}
	}
	return nil
}

// serviceIdentity reads the ARN namespace, IAM prefix and signing name.
func (b *builder) serviceIdentity(svc *Shape) error {
	if raw, ok := svc.Traits[traitAWSService]; ok {
		var info struct {
			ARNNamespace string `json:"arnNamespace"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitAWSService, err)
		}
		b.ir.ARNNamespace = info.ARNNamespace
	}
	b.ir.IAMPrefix = b.cfg.IAMPrefix
	if raw, ok := svc.Traits[traitSigV4]; ok {
		var v struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitSigV4, err)
		}
		b.ir.SigningName = v.Name
	}
	return nil
}

// serviceProtocols lists the model's protocols, then the config's extras,
// and the per-protocol routing facts.
func (b *builder) serviceProtocols(svc *Shape) error {
	ir := b.ir
	for _, id := range svc.Traits.IDs() {
		if p, ok := protocolTraits()[id]; ok {
			ir.Protocols = append(ir.Protocols, p)
		}
	}
	for _, p := range b.cfg.ExtraProtocols {
		if slices.Contains(ir.Protocols, p) {
			return failf(StageIR, string(svc.ID), "extraProtocols repeats %s, which the model already declares", p)
		}
		ir.Protocols = append(ir.Protocols, p)
	}
	if len(ir.Protocols) == 0 {
		return failf(StageIR, string(svc.ID), "declares no protocol")
	}
	if slices.Contains(ir.Protocols, ref.AWSJSON10) || slices.Contains(ir.Protocols, ref.AWSJSON11) {
		ir.JSONTargetPrefix = svc.ID.Name()
	}
	if slices.Contains(ir.Protocols, ref.AWSQuery) {
		ir.QueryVersion = svc.Version
	}
	ir.QueryCompatible = svc.Traits.Has(traitQueryCompatible)
	if raw, ok := svc.Traits[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitXMLNamespace, err)
		}
		ir.XMLNamespace = ns.URI
	}
	return nil
}

// serviceDefaults reads the service-wide overlay defaults members inherit.
func (b *builder) serviceDefaults(svc *Shape) error {
	b.enumDef = EnumClosed
	if raw, ok := svc.Traits[utEmptyCollection]; ok {
		e, err := parseEmptyCollection(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		b.emptyDef = e
	}
	b.ir.PathStyle = PathIndexed
	if raw, ok := svc.Traits[utValidationPathStyle]; ok {
		s, err := parsePathStyle(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		b.ir.PathStyle = s
	}
	return nil
}

func parseXMLNamespace(raw json.RawMessage) (smithy.XMLNamespace, error) {
	var v struct {
		URI    string `json:"uri"`
		Prefix string `json:"prefix"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return smithy.XMLNamespace{}, err
	}
	return smithy.XMLNamespace(v), nil
}
