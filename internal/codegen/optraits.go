package codegen

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

const traitHTTPChecksum = "aws.protocols#httpChecksum"

// checksumHeaderPrefix starts the S3 checksum value headers
// (x-amz-checksum-crc32 and the rest), which aws-chunked uploads may send as
// trailers instead (sigv4-checksums-endpoints.md 6.3).
const checksumHeaderPrefix = "x-amz-checksum-"

// checksums reads aws.protocols#httpChecksum. When an operation names a
// request algorithm member, its checksum value headers may arrive as
// aws-chunked trailers, so their bindings are marked HTTPHeader.Trailer
// and restbind resolves them at the body's EOF.
func (b *builder) checksums() error {
	for _, o := range b.ir.Operations {
		if !o.HasIO() {
			continue
		}
		raw, ok := b.model.Shapes[o.ID].Traits[traitHTTPChecksum]
		if !ok {
			continue
		}
		var v struct {
			RequestAlgorithmMember      string   `json:"requestAlgorithmMember"`
			RequestChecksumRequired     bool     `json:"requestChecksumRequired"`
			RequestValidationModeMember string   `json:"requestValidationModeMember"`
			ResponseAlgorithms          []string `json:"responseAlgorithms"`
		}
		if err := decodeTrait(raw, &v); err != nil {
			return failf(StageIR, string(o.ID), "%s: %w", traitHTTPChecksum, err)
		}
		if v.RequestAlgorithmMember == "" {
			continue
		}
		if _, ok := o.Input.member(v.RequestAlgorithmMember); !ok {
			return failf(StageIR, string(o.ID), "%s names request algorithm member %q, which %s does not have", traitHTTPChecksum, v.RequestAlgorithmMember, o.Input.ID)
		}
		o.Checksum = &ChecksumIR{AlgorithmMember: v.RequestAlgorithmMember}
		for _, m := range o.Input.Members {
			if h, ok := m.HTTP.(smithy.HTTPHeader); ok && strings.HasPrefix(strings.ToLower(h.Name), checksumHeaderPrefix) {
				h.Trailer = true
				m.HTTP = h
			}
		}
	}
	return nil
}

// member finds a member by name.
func (s *ShapeIR) member(name string) (*MemberIR, bool) {
	for _, m := range s.Members {
		if m.Name == name {
			return m, true
		}
	}
	return nil, false
}

// notConfigured reads dev.understudy#notConfigured: the error shape it
// names must be an error of the service, and the default document is
// rendered as a typed output literal when emitted.
func (b *builder) notConfigured() error {
	for _, o := range b.ir.Operations {
		if !o.HasIO() {
			continue
		}
		raw, ok := b.model.Shapes[o.ID].Traits[utNotConfigured]
		if !ok {
			continue
		}
		nc, err := parseNotConfigured(raw)
		if err != nil {
			return failf(StageIR, string(o.ID), "%s: %w", utNotConfigured, err)
		}
		if e, ok := nc.(NotConfiguredError); ok {
			if _, known := b.errors[e.Shape]; !known {
				return failf(StageIR, string(o.ID), "%s names %s, which is not an error structure of %s", utNotConfigured, e.Shape, b.ir.ID)
			}
		}
		o.NotConfigured = nc
	}
	return nil
}
