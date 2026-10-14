package s3

import (
	"crypto/md5" //nolint:gosec // a multipart ETag is the MD5 of the part MD5s by definition; an integrity check, not a security control
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// The multipart checksum rules of s3.md 6.4 and
// s3-checksum-residuals-and-record-backlog.md 1.6 and 1.7.
const (
	typeWithoutAlgorithmMessage = "The x-amz-checksum-type header can only be used with the x-amz-checksum-algorithm header."
	// unsupportedAlgorithmMessage lists the ten algorithms in S3's order
	// (testdata/parity/s3/R08/08-create-alg-foo.json).
	unsupportedAlgorithmMessage = "Checksum algorithm provided is unsupported. Please try again with any of the valid types: [CRC32, CRC32C, CRC64NVME, MD5, SHA1, SHA256, SHA512, XXHASH128, XXHASH3, XXHASH64]"
)

// UploadChecksum is how a multipart upload's object checksum is computed,
// as CreateMultipartUpload declared it, in its snapshot form: both empty
// when Create named no algorithm. checksumMode is its parsed form.
type UploadChecksum struct {
	Algorithm string             `json:"algorithm,omitempty"`
	Type      s3api.ChecksumType `json:"type,omitempty"`
}

// checksumMode is how a multipart upload checksums its object (s3.md 6.4):
// undeclared when CreateMultipartUpload named no algorithm, which gives the
// object a FULL_OBJECT CRC64NVME, or COMPOSITE or FULL_OBJECT under the
// algorithm Create named.
//
//sumtype:decl
type checksumMode interface {
	// partAlgorithm is the algorithm each part is checksummed with.
	partAlgorithm() checksum.Algorithm
}

type undeclaredChecksum struct{}

type compositeChecksum struct{ alg checksum.Algorithm }

type fullObjectChecksum struct{ alg checksum.Algorithm }

func (undeclaredChecksum) partAlgorithm() checksum.Algorithm   { return checksum.CRC64NVME }
func (c compositeChecksum) partAlgorithm() checksum.Algorithm  { return c.alg }
func (f fullObjectChecksum) partAlgorithm() checksum.Algorithm { return f.alg }

// parseUploadChecksum reads CreateMultipartUpload's x-amz-checksum-algorithm
// and x-amz-checksum-type. The type defaults to FULL_OBJECT for CRC64NVME
// and COMPOSITE otherwise; CRC64NVME is FULL_OBJECT only and only the CRC
// family combines into a FULL_OBJECT checksum (s3.md 6.4).
func parseUploadChecksum(alg *s3api.ChecksumAlgorithm, typ *s3api.ChecksumType) (checksumMode, *awserr.Error) {
	if alg == nil {
		if typ != nil {
			return nil, errInvalidRequest(typeWithoutAlgorithmMessage)
		}
		return undeclaredChecksum{}, nil
	}
	a, err := checksum.ParseAlgorithm(string(*alg))
	if err != nil {
		return nil, errInvalidRequest(unsupportedAlgorithmMessage)
	}
	t := s3api.ChecksumTypeComposite
	if a == checksum.CRC64NVME {
		t = s3api.ChecksumTypeFullObject
	}
	if typ != nil {
		t = *typ
	}
	switch t {
	case s3api.ChecksumTypeComposite:
		if a == checksum.CRC64NVME {
			return nil, errInvalidRequest("The COMPOSITE checksum type cannot be used with the " + lower(a) + " checksum algorithm.")
		}
		return compositeChecksum{alg: a}, nil
	case s3api.ChecksumTypeFullObject:
		if !a.Combinable() {
			return nil, errInvalidRequest("The FULL_OBJECT checksum type cannot be used with the " + lower(a) + " checksum algorithm.")
		}
		return fullObjectChecksum{alg: a}, nil
	}
	// The text is UNVERIFIED: s3-checksum-residuals-and-record-backlog.md
	// 1.6 records only the code.
	return nil, errInvalidRequest("Value for x-amz-checksum-type header is invalid.")
}

// lower is an algorithm as S3's multipart texts name it ("crc32").
func lower(a checksum.Algorithm) string { return strings.ToLower(a.String()) }

// persisted is m in its snapshot form.
func persisted(m checksumMode) UploadChecksum {
	switch m := m.(type) {
	case compositeChecksum:
		return UploadChecksum{Algorithm: m.alg.String(), Type: s3api.ChecksumTypeComposite}
	case fullObjectChecksum:
		return UploadChecksum{Algorithm: m.alg.String(), Type: s3api.ChecksumTypeFullObject}
	case undeclaredChecksum:
	}
	return UploadChecksum{}
}

// mode parses the snapshot form back. A value parseUploadChecksum never
// writes is a state defect, which the caller answers as InternalError.
func (u UploadChecksum) mode() (checksumMode, error) {
	if u == (UploadChecksum{}) {
		return undeclaredChecksum{}, nil
	}
	a, err := checksum.ParseAlgorithm(u.Algorithm)
	if err != nil {
		return nil, &UploadChecksumError{Stored: u}
	}
	switch {
	case u.Type == s3api.ChecksumTypeComposite && a != checksum.CRC64NVME:
		return compositeChecksum{alg: a}, nil
	case u.Type == s3api.ChecksumTypeFullObject && a.Combinable():
		return fullObjectChecksum{alg: a}, nil
	}
	return nil, &UploadChecksumError{Stored: u}
}

// UploadChecksumError is an upload record whose checksum algorithm and
// type CreateMultipartUpload could not have written: a defect in the state
// a snapshot loaded or in this package.
type UploadChecksumError struct{ Stored UploadChecksum }

// Error names the stored algorithm and type.
func (e *UploadChecksumError) Error() string {
	return fmt.Sprintf("s3: a multipart upload records checksum algorithm %q with type %q, which CreateMultipartUpload never stores", e.Stored.Algorithm, e.Stored.Type)
}

// declaredOf is the algorithm and type a response reports for m: none for
// an undeclared upload.
func declaredOf(m checksumMode) (*s3api.ChecksumAlgorithm, *s3api.ChecksumType) {
	s := persisted(m)
	if s.Algorithm == "" {
		return nil, nil
	}
	return new(s3api.ChecksumAlgorithm(s.Algorithm)), new(s.Type)
}

// checkPartChecksum refuses, before the body is read, a part whose
// checksum header or trailer names another algorithm than the one its
// upload declared (s3.md 6.4); an undeclared upload accepts any, and a part
// without one is checksummed by S3.
func checkPartChecksum(m checksumMode, d declaredChecksum) *awserr.Error {
	if _, undeclared := m.(undeclaredChecksum); undeclared {
		return nil
	}
	if _, none := d.(noChecksum); none {
		return nil
	}
	if want, got := m.partAlgorithm(), d.algorithm(); got != want {
		return errChecksumTypeMismatch(want, got)
	}
	return nil
}

func errChecksumTypeMismatch(want, got checksum.Algorithm) *awserr.Error {
	return errInvalidRequest("Checksum Type mismatch occurred, expected checksum Type: " + lower(want) + ", actual checksum Type: " + lower(got))
}

// reportedPartChecksum is the part checksum UploadPartCopy and ListParts
// report: the part's checksum under the declared algorithm, and none for
// an undeclared upload, whose CRC64NVME part checksums serve only the
// object's (REC test_complete_multipart_parts_checksum_default step
// list-parts; UploadPartCopy to an undeclared upload is UNVERIFIED).
func reportedPartChecksum(m checksumMode, p Part) checksumMembers {
	if _, undeclared := m.(undeclaredChecksum); undeclared {
		return checksumMembers{}
	}
	return membersOf(StoredChecksum{Algorithm: m.partAlgorithm().String(), Value: p.Checksum})
}

// uploadedPartChecksum is the part checksum UploadPart reports. A part of
// an undeclared upload echoes the checksum it was sent with, verified as
// sent, which sum holds (REC test_complete_multipart_parts_checksum_default
// step upload-part-different-checksum-than-default, test_multipart_and_list_parts
// step upload-part), and reports none when it was sent without one
// (UNVERIFIED: every recording's SDK sends one). A part of a declared
// upload reports as reportedPartChecksum.
func uploadedPartChecksum(m checksumMode, d declaredChecksum, sum checksum.Sum, p Part) checksumMembers {
	if _, undeclared := m.(undeclaredChecksum); !undeclared {
		return reportedPartChecksum(m, p)
	}
	if _, none := d.(noChecksum); none {
		return checksumMembers{}
	}
	return membersOf(StoredChecksum{Algorithm: sum.Algorithm().String(), Value: sum.String()})
}

// listedPart is one Part of a CompleteMultipartUpload body with the
// uploaded part it names.
type listedPart struct {
	number int32
	// etag is the ETag the request sent, unquoted.
	etag string
	// restated are the part checksums the request sent.
	restated checksumMembers
	part     Part
}

// errInvalidPart answers a listed part that names no uploaded part, or
// whose ETag or restated checksum differs from the part's (s3.md 10,
// wire-rest.md 3.6). Both spaces after the first sentence are S3's.
func errInvalidPart(uploadID string, p *listedPart) *awserr.Error {
	e := s3api.ErrInvalidPart("One or more of the specified parts could not be found.  The part may not have been uploaded, or the specified entity tag may not match the part's entity tag.")
	// S3's element order (testdata/parity/s3/R08/06-complete-with-part-checksums.json).
	return withField(withField(withField(e, "UploadId", uploadID), "PartNumber", strconv.Itoa(int(p.number))), "ETag", p.etag)
}

// objectChecksum checks the part checksums a Complete restated against m
// and computes the object's checksum (s3.md 6.4): the parts' CRCs combined
// for FULL_OBJECT, which is what CRC64NVME is for an undeclared upload, or
// the checksum of the concatenated part digests with "-N" for COMPOSITE.
// An undeclared upload refuses any restated part checksum as InvalidPart,
// even the part's own (REC test_complete_multipart_parts_checksum_default
// steps complete-multipart-parts-checksum and
// complete-multipart-wrong-parts-checksum). A COMPOSITE upload must list
// every part from 1 on: S3 answers a gap with 500, a defect this answers
// as InvalidPart instead.
func objectChecksum(m checksumMode, uploadID string, parts []listedPart) (StoredChecksum, *awserr.Error) {
	switch m := m.(type) {
	case undeclaredChecksum:
		for i := range parts {
			if len(parts[i].restated.sent()) > 0 {
				return StoredChecksum{}, errInvalidPart(uploadID, &parts[i])
			}
		}
		return combined(checksum.CRC64NVME, parts)
	case fullObjectChecksum:
		if err := checkRestated(m.alg, uploadID, parts, false); err != nil {
			return StoredChecksum{}, err
		}
		return combined(m.alg, parts)
	case compositeChecksum:
		if err := checkRestated(m.alg, uploadID, parts, true); err != nil {
			return StoredChecksum{}, err
		}
		for i := range parts {
			if int(parts[i].number) != i+1 {
				return StoredChecksum{}, errInvalidPart(uploadID, &parts[i])
			}
		}
		return composite(m.alg, parts)
	}
	return StoredChecksum{}, awserr.Internalf(fmt.Errorf("s3: objectChecksum got checksum mode %T, which parseUploadChecksum never builds", m))
}

// checkRestated checks each listed part's restated checksums: one of
// another algorithm is BadDigest, a wrong value InvalidPart, and a missing
// one InvalidRequest where the upload is COMPOSITE (required).
func checkRestated(alg checksum.Algorithm, uploadID string, parts []listedPart, required bool) *awserr.Error {
	for i := range parts {
		p := &parts[i]
		var value *string
		for _, s := range p.restated.sent() {
			if s.alg != alg {
				return s3api.ErrBadDigest("The " + lower(s.alg) + " you specified for part " + strconv.Itoa(int(p.number)) + " did not match what we received.")
			}
			value = &s.value
		}
		switch {
		case value == nil && required:
			return errInvalidRequest("The upload was created using a " + lower(alg) + " checksum. The complete request must include the checksum for each part. It was missing for part " +
				strconv.Itoa(int(p.number)) + " in the request.")
		case value != nil && *value != p.part.Checksum:
			return errInvalidPart(uploadID, p)
		}
	}
	return nil
}

// partSums decodes the listed parts' checksums under alg.
func partSums(alg checksum.Algorithm, parts []listedPart) ([]checksum.Sum, error) {
	out := make([]checksum.Sum, 0, len(parts))
	for i := range parts {
		p := &parts[i]
		s, err := checksum.Decode(alg, p.part.Checksum)
		if err != nil {
			return nil, fmt.Errorf("s3: part %d records a %s checksum %q that does not decode: %w", p.number, alg, p.part.Checksum, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// combined is the FULL_OBJECT checksum of the listed parts.
func combined(alg checksum.Algorithm, parts []listedPart) (StoredChecksum, *awserr.Error) {
	sums, err := partSums(alg, parts)
	if err != nil {
		return StoredChecksum{}, awserr.Internalf(err)
	}
	cp := make([]checksum.Part, len(parts))
	for i := range parts {
		cp[i] = checksum.Part{Sum: sums[i], Size: parts[i].part.Size}
	}
	sum, err := checksum.Combine(alg, cp)
	if err != nil {
		return StoredChecksum{}, awserr.Internalf(err)
	}
	return StoredChecksum{Algorithm: alg.String(), Value: sum.String(), Type: s3api.ChecksumTypeFullObject}, nil
}

// composite is the COMPOSITE checksum of the listed parts.
func composite(alg checksum.Algorithm, parts []listedPart) (StoredChecksum, *awserr.Error) {
	sums, err := partSums(alg, parts)
	if err != nil {
		return StoredChecksum{}, awserr.Internalf(err)
	}
	value, err := checksum.Composite(alg, sums)
	if err != nil {
		return StoredChecksum{}, awserr.Internalf(err)
	}
	return StoredChecksum{Algorithm: alg.String(), Value: value, Type: s3api.ChecksumTypeComposite}, nil
}

// checkCompleteType refuses an x-amz-checksum-type on Complete other than
// the upload's (s3.md 6.4). An undeclared upload has no mode, which S3
// names "null", so it refuses either type (REC
// test_complete_multipart_parts_checksum_default steps
// complete-multipart-full-object-type and complete-multipart-composite-type).
func checkCompleteType(m checksumMode, t *s3api.ChecksumType) *awserr.Error {
	want := persisted(m).Type
	if t == nil || want != "" && *t == want {
		return nil
	}
	name := string(want)
	if want == "" {
		name = "null"
	}
	return errInvalidRequest("The upload was created using the " + name + " checksum mode. The complete request must use the same checksum mode.")
}

// newerAlgorithms are the algorithms a Complete may not name for an
// upload that declared none (s3-checksum-residuals-and-record-backlog.md
// 1.6).
func newerAlgorithms() []checksum.Algorithm {
	return []checksum.Algorithm{checksum.MD5, checksum.SHA512, checksum.XXHASH64, checksum.XXHASH3, checksum.XXHASH128}
}

// checkCompleteHeader judges the x-amz-checksum-* header a Complete sent
// (at most one, sent) with the x-amz-checksum-type typ against the
// object's computed checksum. An undeclared upload refuses the five newer
// algorithms, accepts CRC32, CRC32C, SHA1 and SHA256 without validating
// them, and verifies CRC64NVME, the checksum it computes. A declared
// upload answers a header of another algorithm as BadDigest naming its
// own, and a FULL_OBJECT upload answers its own algorithm's header the
// same way unless typ is FULL_OBJECT, even when the value is right (REC
// test_multipart_parts_checksum_exceptions_full_object steps
// complete-part-only-checksum-algo, complete-part-only-checksum-algo-diff,
// complete-part-bad-checksum-algo and complete-success; for a COMPOSITE
// upload the other-algorithm rule is LocalStack's, EMU). Otherwise it
// verifies its own: a COMPOSITE value with or without its "-N".
func checkCompleteHeader(m checksumMode, sent *sentChecksum, typ *s3api.ChecksumType, computed StoredChecksum) *awserr.Error {
	if sent == nil {
		return nil
	}
	switch m := m.(type) {
	case undeclaredChecksum:
		switch {
		case sent.alg == checksum.CRC64NVME:
		case slices.Contains(newerAlgorithms(), sent.alg):
			// S3 names the undeclared upload's type "null"
			// (testdata/parity/s3/R08/06-complete-xxhash64-header-no-create-alg.json).
			return errInvalidRequest("Checksum Type mismatch occurred, expected checksum Type: null, actual checksum Type: " + lower(sent.alg))
		default:
			return nil
		}
	case fullObjectChecksum:
		if sent.alg != m.alg || typ == nil || *typ != s3api.ChecksumTypeFullObject {
			return errCompleteDigest(m.alg)
		}
	case compositeChecksum:
		if sent.alg != m.alg {
			return errCompleteDigest(m.alg)
		}
	}
	if computed.Type == s3api.ChecksumTypeComposite {
		digest, _, _ := strings.Cut(computed.Value, "-")
		if sent.value != digest && sent.value != computed.Value {
			return errCompleteDigest(sent.alg)
		}
		return nil
	}
	sum, err := checksum.Decode(sent.alg, computed.Value)
	if err != nil {
		return awserr.Internalf(err)
	}
	verr := checksum.Verify(sent.value, sum)
	if isA[*checksum.InvalidValueError](verr) {
		return errInvalidRequest("Value for " + sent.alg.HeaderName() + " header is invalid.")
	}
	if verr != nil {
		return errCompleteDigest(sent.alg)
	}
	return nil
}

// errCompleteDigest answers a Complete whose object checksum header does
// not match: S3 names the algorithm in lower case here (s3.md 6.4).
func errCompleteDigest(a checksum.Algorithm) *awserr.Error {
	return s3api.ErrBadDigest("The " + lower(a) + " you specified did not match the calculated checksum.")
}

// multipartETag is a multipart object's ETag (s3.md 5.3): the MD5 of the
// concatenated raw part ETags, then "-N". A part ETag is its MD5, or the
// random 128-bit value of an SSE-KMS part, from which S3 derives the
// object's ETag the same way (REC test_s3_multipart_upload_sse).
func multipartETag(parts []listedPart) (string, error) {
	md := md5.New() //nolint:gosec // see the import
	for i := range parts {
		p := &parts[i]
		raw, err := hex.DecodeString(p.part.ETag)
		if err != nil || len(raw) != md5.Size {
			return "", fmt.Errorf("s3: part %d records ETag %q, which is not 128 bits in hex", p.number, p.part.ETag)
		}
		md.Write(raw)
	}
	return hex.EncodeToString(md.Sum(nil)) + "-" + strconv.Itoa(len(parts)), nil
}
