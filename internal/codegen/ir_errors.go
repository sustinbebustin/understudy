package codegen

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// errorIdentities builds an ErrorIR per error structure, each operation's
// error list and the service-level error traits.
func (b *builder) errorIdentities() error {
	svc := b.clo.service
	ns := svc.ID.Namespace()
	if raw, ok := svc.Traits[utErrorNamespace]; ok {
		v, err := parseErrorNamespace(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		ns = v
	}
	for _, s := range b.ir.Shapes {
		if s.Unit || !b.clo.shapes[s.ID].Traits.Has(traitError) {
			continue
		}
		e, err := b.errorIR(s, b.clo.shapes[s.ID], ns)
		if err != nil {
			return err
		}
		s.Error = e
		b.errors[s.ID] = e
		b.ir.Errors = append(b.ir.Errors, e)
	}
	slices.SortFunc(b.ir.Errors, func(a, c *ErrorIR) int { return cmp.Compare(a.Shape.GoName, c.Shape.GoName) })
	if err := b.operationErrors(); err != nil {
		return err
	}
	return b.serviceErrors()
}

// operationErrors lists each operation's modeled errors, then the service's.
func (b *builder) operationErrors() error {
	for _, op := range b.ir.Operations {
		if !op.HasIO() {
			continue
		}
		src := b.model.Shapes[op.ID]
		for _, id := range slices.Concat(src.Errors, b.clo.service.Errors) {
			e, ok := b.errors[id]
			if !ok {
				return failf(StageIR, string(op.ID), "lists %s as an error, but it has no error trait", id)
			}
			op.Errors = append(op.Errors, e)
		}
	}
	return nil
}

// serviceErrors resolves throttleError, malformedRequestError,
// unknownOperationError and the families' generic vocabularies.
func (b *builder) serviceErrors() error {
	for _, v := range b.clo.vocabularies {
		voc := VocabularyIR{Family: v.family}
		for _, id := range v.errors {
			e, ok := b.errors[id]
			if !ok {
				return failf(StageIR, string(v.from), "lists %s under errors, but it has no error trait; the family vocabulary holds error structures only", id)
			}
			voc.Errors = append(voc.Errors, e)
		}
		b.ir.Vocabularies = append(b.ir.Vocabularies, voc)
	}
	var err error
	if b.ir.Throttle, err = b.serviceError(utThrottleError); err != nil {
		return err
	}
	if b.ir.Throttle != nil {
		// The throttle identity is the service's member of the throttling
		// set, which awserr.Identity.Retryable covers with the trait.
		b.ir.Throttle.Retryable = true
	}
	if b.ir.MalformedRequest, err = b.serviceError(utMalformedRequest); err != nil {
		return err
	}
	svc := b.clo.service
	raw, ok := svc.Traits[utUnknownOperation]
	if !ok {
		return nil
	}
	u, err := parseUnknownOperation(raw)
	if err != nil {
		return failf(StageIR, string(svc.ID), "%w", err)
	}
	if b.ir.UnknownJSON, err = b.errorByID(utUnknownOperation, u.JSON); err != nil {
		return err
	}
	b.ir.UnknownQuery, err = b.errorByID(utUnknownOperation, u.Query)
	return err
}

func (b *builder) serviceError(trait string) (*ErrorIR, error) {
	raw, ok := b.clo.service.Traits[trait]
	if !ok {
		return nil, nil //nolint:nilnil // an absent trait has no identity, which is not an error
	}
	id, err := parseShapeRef(raw)
	if err != nil {
		return nil, failf(StageIR, string(b.clo.service.ID), "%s: %w", trait, err)
	}
	return b.errorByID(trait, id)
}

func (b *builder) errorByID(trait string, id smithy.ShapeID) (*ErrorIR, error) {
	if id == "" {
		return nil, nil //nolint:nilnil // an absent reference has no identity, which is not an error
	}
	e, ok := b.errors[id]
	if !ok {
		return nil, failf(StageIR, string(b.clo.service.ID), "%s names %s, which is not an error structure", trait, id)
	}
	return e, nil
}

// errorIR derives an error shape's identity (error-matrix.md 7, D7 to D9):
// status from httpError or the fault default, then statusOverride; the
// Query code from awsQueryError, else the shape name; __type from jsonType
// or the shape name, namespaced for awsJson1_0; the message key from the
// shape's own member spelling.
func (b *builder) errorIR(s *ShapeIR, src *Shape, namespace string) (*ErrorIR, error) {
	subject := string(s.ID)
	e := &ErrorIR{Shape: s, QueryCode: s.ID.Name(), Retryable: src.Traits.Has(traitRetryable), Generic: s.ID.Namespace() != b.cfg.Service.Namespace()}
	if err := e.status(src.Traits); err != nil {
		return nil, failf(StageIR, subject, "%w", err)
	}
	if err := e.queryError(src.Traits); err != nil {
		return nil, failf(StageIR, subject, "%w", err)
	}
	if !slices.Contains(b.ir.Protocols, ref.AWSQuery) && !b.ir.QueryCompatible {
		// awsQueryError's status applies over awsQuery and under
		// x-amzn-query-mode; KMS's legacy traits name a 404 neither reaches.
		e.QueryModeStatus = 0
	}
	name := s.ID.Name()
	if raw, ok := src.Traits[utJSONType]; ok {
		v, err := parseNonEmptyString(raw)
		if err != nil {
			return nil, failf(StageIR, subject, "%w", err)
		}
		name = v
	}
	e.JSONType = name
	if slices.Contains(b.ir.Protocols, ref.AWSJSON10) {
		ns := namespace
		if e.Generic {
			ns = s.ID.Namespace()
		}
		e.JSONType = ns + "#" + name
	}
	e.MessageKey = awserr.NoMessage
	for _, m := range src.Members {
		switch m.Name {
		case "message":
			e.MessageKey = awserr.MessageLower
		case "Message":
			e.MessageKey = awserr.MessageUpper
		}
	}
	if e.MessageKey == awserr.NoMessage && slices.Contains(b.ir.Protocols, ref.RESTXML) {
		// S3's <Error> always carries <Message>, although its error
		// structures declare no members (wire-rest.md 2.4).
		e.MessageKey = awserr.MessageUpper
	}
	if raw, ok := src.Traits[utMessageByProtocol]; ok {
		msgs, err := parseMessageByProtocol(raw)
		if err != nil {
			return nil, failf(StageIR, subject, "%w", err)
		}
		e.Messages = msgs
	}
	return e, nil
}

// status reads the fault, httpError and statusOverride.
func (e *ErrorIR) status(traits Traits) error {
	var fault string
	if err := decodeTrait(traits[traitError], &fault); err != nil {
		return fmt.Errorf("%s: %w", traitError, err)
	}
	switch fault {
	case "client":
		e.Fault, e.Status = awserr.Sender, 400
	case "server":
		e.Fault, e.Status = awserr.Receiver, 500
	default:
		return fmt.Errorf("%s is %q; it must be client or server", traitError, fault)
	}
	if raw, ok := traits[traitHTTPError]; ok {
		if err := decodeTrait(raw, &e.HTTPError); err != nil {
			return fmt.Errorf("%s: %w", traitHTTPError, err)
		}
		e.Status = e.HTTPError
	}
	if raw, ok := traits[utStatusOverride]; ok {
		st, err := parseStatus(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", utStatusOverride, err)
		}
		e.Status = st
	}
	return nil
}

// queryError reads awsQueryError: the Query code, and the status under
// x-amzn-query-mode when it differs from the JSON status.
func (e *ErrorIR) queryError(traits Traits) error {
	raw, ok := traits[traitQueryError]
	if !ok {
		return nil
	}
	var v struct {
		Code             string `json:"code"`
		HTTPResponseCode int    `json:"httpResponseCode"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return fmt.Errorf("%s: %w", traitQueryError, err)
	}
	e.QueryCode = v.Code
	if v.HTTPResponseCode != e.Status {
		e.QueryModeStatus = v.HTTPResponseCode
	}
	return nil
}
