package codegen

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Trait ids the generator reads. Everything else is classified below and
// either ignored or used only for documentation.
const (
	traitRequired        = "smithy.api#required"
	traitDefault         = "smithy.api#default"
	traitLength          = "smithy.api#length"
	traitRange           = "smithy.api#range"
	traitPattern         = "smithy.api#pattern"
	traitEnumValue       = "smithy.api#enumValue"
	traitTimestampFormat = "smithy.api#timestampFormat"
	traitJSONName        = "smithy.api#jsonName"
	traitXMLName         = "smithy.api#xmlName"
	traitXMLFlattened    = "smithy.api#xmlFlattened"
	traitXMLAttribute    = "smithy.api#xmlAttribute"
	traitXMLNamespace    = "smithy.api#xmlNamespace"
	traitHTTP            = "smithy.api#http"
	traitHTTPLabel       = "smithy.api#httpLabel"
	traitHTTPQuery       = "smithy.api#httpQuery"
	traitHTTPQueryParams = "smithy.api#httpQueryParams"
	traitHTTPHeader      = "smithy.api#httpHeader"
	traitHTTPPrefix      = "smithy.api#httpPrefixHeaders"
	traitHTTPPayload     = "smithy.api#httpPayload"
	traitHTTPResponse    = "smithy.api#httpResponseCode"
	traitError           = "smithy.api#error"
	traitHTTPError       = "smithy.api#httpError"
	traitRetryable       = "smithy.api#retryable"
	traitSensitive       = "smithy.api#sensitive"
	traitStreaming       = "smithy.api#streaming"
	traitSparse          = "smithy.api#sparse"
	traitMediaType       = "smithy.api#mediaType"
	traitReadonly        = "smithy.api#readonly"
	traitIdempotent      = "smithy.api#idempotent"
	traitUnitType        = "smithy.api#unitType"
	traitExamples        = "smithy.api#examples"
	traitAWSService      = "aws.api#service"
	traitSigV4           = "aws.auth#sigv4"
	traitQueryError      = "aws.protocols#awsQueryError"
	traitQueryCompatible = "aws.protocols#awsQueryCompatible"
	traitAWSJSON10       = "aws.protocols#awsJson1_0"
	traitAWSJSON11       = "aws.protocols#awsJson1_1"
	traitAWSQuery        = "aws.protocols#awsQuery"
	traitRESTJSON1       = "aws.protocols#restJson1"
	traitRESTXML         = "aws.protocols#restXml"
	traitRPCv2CBOR       = "smithy.protocols#rpcv2Cbor"
)

// Class says what the generator does with a trait (smithy-models.md 4.3).
type Class uint8

// Class values.
const (
	// Honor: the trait changes the wire or validation; the generator carries
	// it into the emitted code, or the service honors it by hand where the
	// table says so.
	Honor Class = iota + 1
	// GenerationOnly: descriptors, documentation or coverage use it; it has no
	// server-observable effect of its own.
	GenerationOnly
	// Ignore: client-side concerns (endpoint rules, waiters, smoke tests,
	// client optionality) the server has no use for.
	Ignore
)

// Classify returns the class of a trait id. An id it does not know returns
// false, and the generator fails: a model bump that brings a new trait
// surfaces as a failing go generate, not as a silently dropped behavior.
func Classify(id string) (Class, bool) {
	if c, ok := traitClasses()[id]; ok {
		return c, true
	}
	for _, ns := range []string{"smithy.rules#", "smithy.test#", "smithy.waiters#"} {
		if strings.HasPrefix(id, ns) {
			return Ignore, true
		}
	}
	if strings.HasPrefix(id, understudyNamespace) {
		if _, ok := understudyTraits()[id]; ok {
			return GenerationOnly, true
		}
	}
	return 0, false
}

// traitClasses is the closed table of every trait id the twelve vendored
// models use, plus the prelude and corpus traits the generator reads.
// idempotencyToken is honored by the services by hand (they deduplicate);
// the traits in notYetEmitted are honored but their emission lands with the
// all-models generator, so a model using them fails until then.
func traitClasses() map[string]Class {
	return map[string]Class{
		traitRequired:                   Honor,
		traitDefault:                    Honor,
		traitLength:                     Honor,
		traitRange:                      Honor,
		traitPattern:                    Honor,
		traitEnumValue:                  Honor,
		traitTimestampFormat:            Honor,
		traitJSONName:                   Honor,
		traitXMLName:                    Honor,
		traitXMLFlattened:               Honor,
		traitXMLAttribute:               Honor,
		traitXMLNamespace:               Honor,
		traitHTTP:                       Honor,
		traitHTTPLabel:                  Honor,
		traitHTTPQuery:                  Honor,
		traitHTTPQueryParams:            Honor,
		traitHTTPHeader:                 Honor,
		traitHTTPPrefix:                 Honor,
		traitHTTPPayload:                Honor,
		traitHTTPResponse:               Honor,
		traitError:                      Honor,
		traitHTTPError:                  Honor,
		traitSensitive:                  Honor,
		traitStreaming:                  Honor,
		traitSparse:                     Honor,
		traitMediaType:                  Honor,
		traitUnitType:                   Honor,
		traitQueryError:                 Honor,
		traitQueryCompatible:            Honor,
		traitAWSJSON10:                  Honor,
		traitAWSJSON11:                  Honor,
		traitAWSQuery:                   Honor,
		traitRESTJSON1:                  Honor,
		traitRESTXML:                    Honor,
		traitRPCv2CBOR:                  Honor,
		"smithy.api#idempotencyToken":   Honor,
		"smithy.api#eventPayload":       Honor,
		"smithy.api#requestCompression": Honor,
		"aws.protocols#httpChecksum":    Honor,
		"aws.customizations#s3UnwrappedXmlOutput": Honor,

		traitRetryable:                         GenerationOnly,
		traitReadonly:                          GenerationOnly,
		traitIdempotent:                        GenerationOnly,
		traitExamples:                          GenerationOnly,
		traitAWSService:                        GenerationOnly,
		traitSigV4:                             GenerationOnly,
		"smithy.api#input":                     GenerationOnly,
		"smithy.api#output":                    GenerationOnly,
		"smithy.api#paginated":                 GenerationOnly,
		"smithy.api#deprecated":                GenerationOnly,
		"smithy.api#documentation":             GenerationOnly,
		"smithy.api#externalDocumentation":     GenerationOnly,
		"smithy.api#auth":                      GenerationOnly,
		"smithy.api#optionalAuth":              GenerationOnly,
		"smithy.api#resourceIdentifier":        GenerationOnly,
		"aws.auth#sigv4a":                      GenerationOnly,
		"aws.auth#unsignedPayload":             GenerationOnly,
		"aws.api#arn":                          GenerationOnly,
		"aws.api#data":                         GenerationOnly,
		"aws.api#tagEnabled":                   GenerationOnly,
		"aws.api#taggable":                     GenerationOnly,
		"aws.iam#iamAction":                    GenerationOnly,
		"aws.iam#iamResource":                  GenerationOnly,
		"aws.iam#conditionKeys":                GenerationOnly,
		"aws.iam#defineConditionKeys":          GenerationOnly,
		"aws.iam#disableConditionKeyInference": GenerationOnly,
		"aws.iam#supportedPrincipalTypes":      GenerationOnly,

		"aws.api#clientDiscoveredEndpoint": Ignore,
		"aws.api#clientEndpointDiscovery":  Ignore,
		"smithy.api#endpoint":              Ignore,
		"smithy.api#hostLabel":             Ignore,
		"smithy.api#clientOptional":        Ignore,
		"smithy.api#addedDefault":          Ignore,
		"smithy.api#tags":                  Ignore,
		"smithy.api#suppress":              Ignore,
		"smithy.api#title":                 Ignore,
	}
}

// notYetEmitted are honored traits, model and overlay alike, whose emission
// belongs to the all-models generator (PHASE1-PLAN T19). A closure that uses
// one fails rather than generating code that silently drops the behavior;
// the overlay traits are still parsed and site-checked at merge.
func notYetEmitted() []string {
	return []string{
		"smithy.api#eventPayload",
		"smithy.api#requestCompression",
		"aws.protocols#httpChecksum",
		"aws.customizations#s3UnwrappedXmlOutput",
		utQueryKeyAlias,
		utNotConfigured,
	}
}

// canonicalJSON re-encodes a JSON value with sorted object keys and exact
// number text, so two trait values compare equal when they mean the same.
func canonicalJSON(raw json.RawMessage) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// decodeTrait decodes a trait value strictly into v.
func decodeTrait(raw json.RawMessage, v any) error {
	return strictUnmarshal(raw, v)
}
