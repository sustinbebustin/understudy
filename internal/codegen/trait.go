package codegen

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Trait ids the generator reads by name. traitClasses classifies these and
// every other trait the models use.
const (
	traitRequired        = "smithy.api#required"
	traitDefault         = "smithy.api#default"
	traitLength          = "smithy.api#length"
	traitRange           = "smithy.api#range"
	traitPattern         = "smithy.api#pattern"
	traitEnumValue       = "smithy.api#enumValue"
	traitEnum            = "smithy.api#enum"
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
	if t, ok := understudyTraits()[id]; ok {
		return t.class, true
	}
	return 0, false
}

// traitClasses is the closed table of every trait id the twelve vendored
// models and the protocol compliance corpus use. Some honored traits have
// no runtime descriptor field and are honored by hand where they act:
// idempotencyToken by the services (they deduplicate), eventPayload by the
// handler that frames an event stream, requestCompression by the awsjson,
// awsquery and rpcv2cbor codecs (gzip accepted on every operation, a
// superset of PutMetricData's), s3UnwrappedXmlOutput by the restxml codec
// (GetBucketLocation), and httpChecksum by the S3 service, while the
// generator marks the checksum headers it names as trailer-capable
// (HTTPHeader.Trailer).
func traitClasses() map[string]Class {
	return map[string]Class{
		traitRequired:                   Honor,
		traitDefault:                    Honor,
		traitLength:                     Honor,
		traitRange:                      Honor,
		traitPattern:                    Honor,
		traitEnumValue:                  Honor,
		traitEnum:                       Honor,
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

		// Traits only the protocol compliance corpus uses. eventHeader and
		// httpChecksumRequired are honored by hand like eventPayload and
		// httpChecksum. uniqueItems has no AWS model use and no AWS error
		// text; the corpus's uniqueItems cases are skip-listed with the
		// smithy-rs reason (smithy-protocol-tests.md 4.2). The rest describe
		// trait definitions, ec2Query and rpcv2Json, which no generated
		// service speaks, or client concerns.
		"smithy.api#eventHeader":          Honor,
		"smithy.api#httpChecksumRequired": Honor,
		"smithy.api#uniqueItems":          Ignore,
		"smithy.api#internal":             GenerationOnly,
		"smithy.api#recommended":          GenerationOnly,
		"smithy.api#requiresLength":       GenerationOnly,
		"smithy.api#unstable":             GenerationOnly,
		"smithy.api#idRef":                Ignore,
		"smithy.api#private":              Ignore,
		"smithy.api#trait":                Ignore,
		"smithy.api#traitValidators":      Ignore,
		"smithy.api#protocolDefinition":   Ignore,
		"smithy.api#authDefinition":       Ignore,
		"aws.protocols#ec2Query":          Ignore,
		"aws.protocols#ec2QueryName":      Ignore,
		"smithy.protocols#rpcv2Json":      Ignore,

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
