package s3_test

import (
	"bytes"
	"net/http"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// headerFor is a PutObject input carrying value as the x-amz-checksum-*
// header of alg.
func headerFor(alg checksum.Algorithm, value string) s3api.PutObjectInput {
	var in s3api.PutObjectInput
	m := map[checksum.Algorithm]**string{
		checksum.CRC32: &in.ChecksumCRC32, checksum.CRC32C: &in.ChecksumCRC32C, checksum.CRC64NVME: &in.ChecksumCRC64NVME,
		checksum.SHA1: &in.ChecksumSHA1, checksum.SHA256: &in.ChecksumSHA256, checksum.SHA512: &in.ChecksumSHA512, checksum.MD5: &in.ChecksumMD5,
		checksum.XXHASH64: &in.ChecksumXXHASH64, checksum.XXHASH3: &in.ChecksumXXHASH3, checksum.XXHASH128: &in.ChecksumXXHASH128,
	}
	*m[alg] = &value
	return in
}

// partHeaderFor is an UploadPart input carrying value as the
// x-amz-checksum-* header of alg.
func partHeaderFor(alg checksum.Algorithm, value string) s3api.UploadPartInput {
	p := headerFor(alg, value)
	return s3api.UploadPartInput{
		ChecksumCRC32: p.ChecksumCRC32, ChecksumCRC32C: p.ChecksumCRC32C, ChecksumCRC64NVME: p.ChecksumCRC64NVME,
		ChecksumSHA1: p.ChecksumSHA1, ChecksumSHA256: p.ChecksumSHA256, ChecksumSHA512: p.ChecksumSHA512, ChecksumMD5: p.ChecksumMD5,
		ChecksumXXHASH64: p.ChecksumXXHASH64, ChecksumXXHASH3: p.ChecksumXXHASH3, ChecksumXXHASH128: p.ChecksumXXHASH128,
	}
}

// reported is the one checksum member an output sets, by algorithm.
func reported(members ...*string) map[checksum.Algorithm]string {
	out := map[checksum.Algorithm]string{}
	for i, a := range checksum.Algorithms() {
		if members[i] != nil {
			out[a] = *members[i]
		}
	}
	return out
}

// TestEveryAlgorithmOnSinglePartWrites: PutObject and UploadPart accept a
// header checksum of each of the ten algorithms (s3.md 6.1), verify it,
// store it and report it; a wrong value is BadDigest naming the algorithm
// (s3.md 6.2).
func TestEveryAlgorithmOnSinglePartWrites(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("algorithms")
		body := []byte("every flexible checksum algorithm")
		for _, alg := range checksum.Algorithms() {
			want := sumOf(t, alg, body)
			in := headerFor(alg, want)
			in.Bucket, in.Key, in.Body = "algorithms", alg.String(), bytesBody(string(body))
			out := ok(h, who{}, s3api.OpPutObject, &in)
			got := reported(out.ChecksumCRC32, out.ChecksumCRC32C, out.ChecksumCRC64NVME, out.ChecksumSHA1, out.ChecksumSHA256,
				out.ChecksumSHA512, out.ChecksumMD5, out.ChecksumXXHASH64, out.ChecksumXXHASH3, out.ChecksumXXHASH128)
			if len(got) != 1 || got[alg] != want {
				t.Errorf("PutObject with %s reports %v, want %s alone", alg, got, want)
			}
			_, get := h.read(&s3api.GetObjectInput{Bucket: "algorithms", Key: alg.String(), ChecksumMode: new(s3api.ChecksumModeEnabled)})
			if g := reported(get.ChecksumCRC32, get.ChecksumCRC32C, get.ChecksumCRC64NVME, get.ChecksumSHA1, get.ChecksumSHA256,
				get.ChecksumSHA512, get.ChecksumMD5, get.ChecksumXXHASH64, get.ChecksumXXHASH3, get.ChecksumXXHASH128); g[alg] != want {
				t.Errorf("GetObject of the %s object reports %v, want %s", alg, g, want)
			}

			wrong := headerFor(alg, sumOf(t, alg, []byte("other")))
			wrong.Bucket, wrong.Key, wrong.Body = "algorithms", "wrong", bytesBody(string(body))
			_, err := as(h, who{}, s3api.OpPutObject, &wrong)
			wantCode(t, err, "BadDigest", http.StatusBadRequest)
			if want := "The " + alg.String() + " you specified did not match the calculated checksum."; err.Message != want {
				t.Errorf("wrong %s: message %q, want %q", alg, err.Message, want)
			}

			id := h.startUpload("algorithms", "mpu-"+alg.String(), s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithm(alg.String()))})
			part := h.sendPart("algorithms", "mpu-"+alg.String(), id, 1, body, partHeaderFor(alg, want))
			if p := reported(part.ChecksumCRC32, part.ChecksumCRC32C, part.ChecksumCRC64NVME, part.ChecksumSHA1, part.ChecksumSHA256,
				part.ChecksumSHA512, part.ChecksumMD5, part.ChecksumXXHASH64, part.ChecksumXXHASH3, part.ChecksumXXHASH128); len(p) != 1 || p[alg] != want {
				t.Errorf("UploadPart with %s reports %v, want %s alone", alg, p, want)
			}
		}
	})
}

// TestCreateMultipartUploadChecksumTypes is the CreateMultipartUpload
// matrix of s3.md 6.4: the default type is FULL_OBJECT for CRC64NVME and
// COMPOSITE otherwise; CRC64NVME is never COMPOSITE, only the CRC family
// is FULL_OBJECT, and a type needs an algorithm.
func TestCreateMultipartUploadChecksumTypes(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("types")
		for _, tc := range []struct {
			alg      s3api.ChecksumAlgorithm
			typ      s3api.ChecksumType
			wantType s3api.ChecksumType
			message  string
		}{
			{alg: s3api.ChecksumAlgorithmCrc32, wantType: s3api.ChecksumTypeComposite},
			{alg: s3api.ChecksumAlgorithmCrc64nvme, wantType: s3api.ChecksumTypeFullObject},
			{alg: s3api.ChecksumAlgorithmSha256, wantType: s3api.ChecksumTypeComposite},
			{alg: s3api.ChecksumAlgorithmXxhash3, wantType: s3api.ChecksumTypeComposite},
			{alg: s3api.ChecksumAlgorithmCrc32c, typ: s3api.ChecksumTypeFullObject, wantType: s3api.ChecksumTypeFullObject},
			{alg: s3api.ChecksumAlgorithmCrc32, typ: s3api.ChecksumTypeComposite, wantType: s3api.ChecksumTypeComposite},
			{alg: s3api.ChecksumAlgorithmSha256, typ: s3api.ChecksumTypeFullObject, message: "The FULL_OBJECT checksum type cannot be used with the sha256 checksum algorithm."},
			{alg: s3api.ChecksumAlgorithmCrc64nvme, typ: s3api.ChecksumTypeComposite, message: "The COMPOSITE checksum type cannot be used with the crc64nvme checksum algorithm."},
			{typ: s3api.ChecksumTypeComposite, message: "The x-amz-checksum-type header can only be used with the x-amz-checksum-algorithm header."},
		} {
			in := &s3api.CreateMultipartUploadInput{Bucket: "types", Key: "k"}
			if tc.alg != "" {
				in.ChecksumAlgorithm = new(tc.alg)
			}
			if tc.typ != "" {
				in.ChecksumType = new(tc.typ)
			}
			out, err := as(h, who{}, s3api.OpCreateMultipartUpload, in)
			if tc.message != "" {
				if err == nil || err.ID.QueryCode != "InvalidRequest" || err.Message != tc.message {
					t.Errorf("%s %s = %v, want InvalidRequest %q", tc.alg, tc.typ, err, tc.message)
				}
				continue
			}
			if err != nil || *out.ChecksumAlgorithm != tc.alg || *out.ChecksumType != tc.wantType {
				t.Errorf("%s %s = %+v %v, want %s %s echoed", tc.alg, tc.typ, out, err, tc.alg, tc.wantType)
			}
		}
		out := ok(h, who{}, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{Bucket: "types", Key: "k"})
		if out.ChecksumAlgorithm != nil || out.ChecksumType != nil {
			t.Errorf("an upload without an algorithm echoes %v %v, want neither", out.ChecksumAlgorithm, out.ChecksumType)
		}
	})
}

// completeCase is one refused CompleteMultipartUpload: the parts it lists
// and its other members, and the error S3 answers, fields included.
type completeCase struct {
	name    string
	parts   []s3api.CompletedPart
	in      s3api.CompleteMultipartUploadInput
	code    string
	message string
	fields  map[string]string
}

// refuseCompletes runs each case against upload id of bucket/key and
// checks its error exactly.
func refuseCompletes(h *harness, bucket, key, id string, cases []completeCase) {
	h.Helper()
	for i := range cases {
		tc := &cases[i]
		_, err := complete(h, bucket, key, id, tc.parts, tc.in)
		if err == nil || err.ID.QueryCode != tc.code || err.ID.Status != http.StatusBadRequest || err.Message != tc.message {
			h.Errorf("%s = %v, want 400 %s %q", tc.name, err, tc.code, tc.message)
			continue
		}
		wantFields(h.T, err, tc.fields)
	}
}

// TestCompleteMultipartUploadChecksumRules is the Complete side of s3.md
// 6.4, as recorded (REC TestS3MultipartUploadChecksum at LocalStack
// 8b9a79f): a COMPOSITE upload needs each part's checksum restated; one of
// another algorithm is BadDigest, a wrong one InvalidPart; the checksum
// type must be the upload's, judged before the parts are; a COMPOSITE list
// must start at part 1; a FULL_OBJECT header counts only with type
// FULL_OBJECT; an upload without an algorithm echoes a part's checksum on
// UploadPart only, refuses restated part checksums and either type, refuses
// the newer algorithms on Complete and ignores the older ones.
func TestCompleteMultipartUploadChecksumRules(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("rules")
		body := []byte("abc")
		crc32 := h.startUpload("rules", "crc32", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32)})
		part := h.sendPart("rules", "crc32", crc32, 1, body, s3api.UploadPartInput{})
		bare := s3api.CompletedPart{PartNumber: new(int32(1)), ETag: part.ETag}
		compositeMode := "The upload was created using the COMPOSITE checksum mode. The complete request must use the same checksum mode."
		refuseCompletes(h, "rules", "crc32", crc32, []completeCase{
			{
				name: "not restated", parts: []s3api.CompletedPart{bare}, code: "InvalidRequest",
				message: "The upload was created using a crc32 checksum. The complete request must include the checksum for each part. It was missing for part 1 in the request.",
			},
			{
				name:  "another algorithm restated",
				parts: []s3api.CompletedPart{{PartNumber: bare.PartNumber, ETag: bare.ETag, ChecksumSHA256: new(sumOf(t, checksum.SHA256, body))}},
				code:  "BadDigest", message: "The sha256 you specified for part 1 did not match what we received.",
			},
			{
				name:  "wrong value restated",
				parts: []s3api.CompletedPart{{PartNumber: bare.PartNumber, ETag: bare.ETag, ChecksumCRC32: new("AAAAAA==")}},
				code:  "InvalidPart", message: invalidPartMessage,
				fields: map[string]string{"ETag": unquote(*part.ETag), "PartNumber": "1", "UploadId": crc32},
			},
			{
				name: "another type", parts: []s3api.CompletedPart{listed(1, part)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumType: new(s3api.ChecksumTypeFullObject)},
				code: "InvalidRequest", message: compositeMode,
			},
			{
				name: "another type before an unknown part", parts: []s3api.CompletedPart{listed(9, part)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumType: new(s3api.ChecksumTypeFullObject)},
				code: "InvalidRequest", message: compositeMode,
			},
			{
				name: "another algorithm's object header", parts: []s3api.CompletedPart{listed(1, part)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new(sumOf(t, checksum.CRC32C, body))},
				code: "BadDigest", message: "The crc32 you specified did not match the calculated checksum.",
			},
		})

		gap := h.startUpload("rules", "gap", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmSha1)})
		second := h.sendPart("rules", "gap", gap, 2, body, s3api.UploadPartInput{})
		_, err := complete(h, "rules", "gap", gap, []s3api.CompletedPart{listed(2, second)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "InvalidPart", http.StatusBadRequest)

		// REC test_multipart_parts_checksum_exceptions_full_object.
		full := h.startUpload("rules", "full", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32c), ChecksumType: new(s3api.ChecksumTypeFullObject)})
		fp := h.sendPart("rules", "full", full, 1, body, s3api.UploadPartInput{})
		fullObject := new(s3api.ChecksumTypeFullObject)
		crc32c := sumOf(t, checksum.CRC32C, body)
		crc32cDigest := "The crc32c you specified did not match the calculated checksum."
		refuseCompletes(h, "rules", "full", full, []completeCase{
			{
				name: "complete-part-bad-checksum-type", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumType: new(s3api.ChecksumTypeComposite)},
				code: "InvalidRequest", message: "The upload was created using the FULL_OBJECT checksum mode. The complete request must use the same checksum mode.",
			},
			{
				name: "complete-part-only-checksum-algo", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new(crc32c)},
				code: "BadDigest", message: crc32cDigest,
			},
			{
				name: "complete-part-good-checksum-no-type", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new(compositeOf(t, checksum.CRC32C, body))},
				code: "BadDigest", message: crc32cDigest,
			},
			{
				name: "complete-part-only-checksum-algo-diff", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC64NVME: new(sumOf(t, checksum.CRC64NVME, body))},
				code: "BadDigest", message: crc32cDigest,
			},
			{
				name: "complete-part-bad-checksum", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new(sumOf(t, checksum.CRC32C, []byte("bad string"))), ChecksumType: fullObject},
				code: "BadDigest", message: crc32cDigest,
			},
			{
				name: "complete-part-bad-checksum-algo", parts: []s3api.CompletedPart{listed(1, fp)},
				in:   s3api.CompleteMultipartUploadInput{ChecksumCRC32: new(sumOf(t, checksum.CRC32, []byte("bad string"))), ChecksumType: fullObject},
				code: "BadDigest", message: crc32cDigest,
			},
		})
		done, err := complete(h, "rules", "full", full, []s3api.CompletedPart{listed(1, fp)}, s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new(crc32c), ChecksumType: fullObject})
		if err != nil || *done.ChecksumCRC32C != crc32c || *done.ChecksumType != s3api.ChecksumTypeFullObject {
			t.Fatalf("complete-success = %+v %v, want CRC32C of the body, FULL_OBJECT", done, err)
		}

		// REC test_complete_multipart_parts_checksum_default.
		plain := h.startUpload("rules", "plain", s3api.CreateMultipartUploadInput{})
		pp := h.sendPart("rules", "plain", plain, 1, body, s3api.UploadPartInput{ChecksumCRC32C: new(crc32c)})
		if got := reported(pp.ChecksumCRC32, pp.ChecksumCRC32C, pp.ChecksumCRC64NVME, pp.ChecksumSHA1, pp.ChecksumSHA256,
			pp.ChecksumSHA512, pp.ChecksumMD5, pp.ChecksumXXHASH64, pp.ChecksumXXHASH3, pp.ChecksumXXHASH128); len(got) != 1 || got[checksum.CRC32C] != crc32c {
			t.Errorf("a part of an upload without an algorithm reports %v, want its CRC32C %s echoed alone", got, crc32c)
		}
		lp := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "rules", Key: "plain", UploadId: plain})
		if p := lp.Parts[0]; p.ChecksumCRC32C != nil || p.ChecksumCRC64NVME != nil || lp.ChecksumAlgorithm != nil || lp.ChecksumType != nil {
			t.Errorf("ListParts of an upload without an algorithm = part %+v, %v %v; want no checksum, algorithm or type", p, lp.ChecksumAlgorithm, lp.ChecksumType)
		}
		plainBare := s3api.CompletedPart{PartNumber: new(int32(1)), ETag: pp.ETag}
		partFields := map[string]string{"ETag": unquote(*pp.ETag), "PartNumber": "1", "UploadId": plain}
		nullMode := "The upload was created using the null checksum mode. The complete request must use the same checksum mode."
		refuseCompletes(h, "rules", "plain", plain, []completeCase{
			{
				name: "complete-multipart-parts-checksum", parts: []s3api.CompletedPart{listed(1, pp)},
				code: "InvalidPart", message: invalidPartMessage, fields: partFields,
			},
			{
				name:  "complete-multipart-wrong-parts-checksum",
				parts: []s3api.CompletedPart{{PartNumber: plainBare.PartNumber, ETag: plainBare.ETag, ChecksumSHA256: new(sumOf(t, checksum.SHA256, body))}},
				code:  "InvalidPart", message: invalidPartMessage, fields: partFields,
			},
			{
				name: "complete-multipart-full-object-type", parts: []s3api.CompletedPart{plainBare},
				in:   s3api.CompleteMultipartUploadInput{ChecksumType: fullObject},
				code: "InvalidRequest", message: nullMode,
			},
			{
				name: "complete-multipart-composite-type", parts: []s3api.CompletedPart{plainBare},
				in:   s3api.CompleteMultipartUploadInput{ChecksumType: new(s3api.ChecksumTypeComposite)},
				code: "InvalidRequest", message: nullMode,
			},
			{
				// testdata/parity/s3/R08/06-complete-xxhash64-header-no-create-alg.json
				name: "a newer algorithm's object header", parts: []s3api.CompletedPart{plainBare},
				in:   s3api.CompleteMultipartUploadInput{ChecksumXXHASH64: new(sumOf(t, checksum.XXHASH64, body))},
				code: "InvalidRequest", message: "Checksum Type mismatch occurred, expected checksum Type: null, actual checksum Type: xxhash64",
			},
		})
		done, err = complete(h, "rules", "plain", plain, []s3api.CompletedPart{plainBare}, s3api.CompleteMultipartUploadInput{ChecksumCRC32C: new("AAAAAA==-2")})
		if err != nil || *done.ChecksumCRC64NVME != sumOf(t, checksum.CRC64NVME, body) || done.ChecksumCRC32C != nil || *done.ChecksumType != s3api.ChecksumTypeFullObject {
			t.Fatalf("complete-multipart-checksum = %+v %v, want the CRC32C header ignored and CRC64NVME FULL_OBJECT", done, err)
		}
	})
}

// uploadMode is how a CreateMultipartUpload declares its checksum: an
// algorithm and type, or neither.
type uploadMode struct {
	alg checksum.Algorithm
	typ s3api.ChecksumType
}

// uploadModes are every mode S3 accepts (s3.md 6.4).
func uploadModes() []uploadMode {
	modes := []uploadMode{{}}
	for _, a := range checksum.Algorithms() {
		if a != checksum.CRC64NVME {
			modes = append(modes, uploadMode{alg: a, typ: s3api.ChecksumTypeComposite})
		}
		if a.Combinable() {
			modes = append(modes, uploadMode{alg: a, typ: s3api.ChecksumTypeFullObject})
		}
	}
	return modes
}

// TestMultipartMatchesSinglePart is the property of s3.md 6.4: random
// part splits of a random body complete to an object whose bytes equal the
// body, whose ETag is the MD5 of the part MD5s with "-N", and whose
// checksum, for a FULL_OBJECT upload of every CRC algorithm (CRC64NVME
// when none is declared), equals the body's single-part checksum, and for
// a COMPOSITE upload of every algorithm equals the per-part computation.
// Parts sometimes carry their checksum and sometimes leave it to S3.
func TestMultipartMatchesSinglePart(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("split")
		rapid.Check(h, func(rt *rapid.T) {
			mode := rapid.SampledFrom(uploadModes()).Draw(rt, "mode")
			n := rapid.IntRange(1, 3).Draw(rt, "parts")
			sizes := make([]int, n)
			for i := range n - 1 {
				sizes[i] = minPart + rapid.IntRange(0, 4096).Draw(rt, "extra")
			}
			sizes[n-1] = rapid.IntRange(0, 1<<16).Draw(rt, "last")
			total := 0
			for _, s := range sizes {
				total += s
			}
			body := payload(rapid.Uint64().Draw(rt, "seed"), total)
			parts := make([][]byte, n)
			for i, off := 0, 0; i < n; i, off = i+1, off+sizes[i] {
				parts[i] = body[off : off+sizes[i]]
			}

			create := s3api.CreateMultipartUploadInput{}
			partAlg := checksum.CRC64NVME
			if mode.alg != 0 {
				create.ChecksumAlgorithm, create.ChecksumType = new(s3api.ChecksumAlgorithm(mode.alg.String())), new(mode.typ)
				partAlg = mode.alg
			}
			id := h.startUpload("split", "obj", create)
			list := make([]s3api.CompletedPart, n)
			for i, p := range parts {
				var in s3api.UploadPartInput
				if mode.alg != 0 && rapid.Bool().Draw(rt, "send part checksum") {
					in = partHeaderFor(mode.alg, sumOf(rt, mode.alg, p))
				}
				out, err := uploadPart(h, "split", "obj", id, int32(i+1), p, in)
				if err != nil {
					rt.Fatalf("UploadPart %d: %s %s", i+1, err.ID.QueryCode, err.Message)
				}
				list[i] = listed(int32(i+1), out)
			}
			done, err := complete(h, "split", "obj", id, list, s3api.CompleteMultipartUploadInput{})
			if err != nil {
				rt.Fatalf("Complete: %s %s", err.ID.QueryCode, err.Message)
			}
			if *done.ETag != multipartETag(parts...) {
				rt.Fatalf("ETag = %s, want %s", *done.ETag, multipartETag(parts...))
			}
			got := reported(done.ChecksumCRC32, done.ChecksumCRC32C, done.ChecksumCRC64NVME, done.ChecksumSHA1, done.ChecksumSHA256,
				done.ChecksumSHA512, done.ChecksumMD5, done.ChecksumXXHASH64, done.ChecksumXXHASH3, done.ChecksumXXHASH128)
			want, wantType := sumOf(rt, partAlg, body), s3api.ChecksumTypeFullObject
			if mode.typ == s3api.ChecksumTypeComposite {
				want, wantType = compositeOf(rt, partAlg, parts...), s3api.ChecksumTypeComposite
			}
			if len(got) != 1 || got[partAlg] != want || *done.ChecksumType != wantType {
				rt.Fatalf("%s %s checksum = %v %s, want %s %s", partAlg, mode.typ, got, *done.ChecksumType, want, wantType)
			}
			stored, _ := h.readBytes(&s3api.GetObjectInput{Bucket: "split", Key: "obj"})
			if !bytes.Equal(stored, body) {
				rt.Fatalf("object = %d bytes, want the %d bytes of the body", len(stored), len(body))
			}
			if refs := blobs.Refs(); len(refs) != 1 {
				rt.Fatalf("blob store holds %d blobs, want the object's alone", len(refs))
			}
		})
	})
}

// TestCompositeChecksumVectors pins the COMPOSITE values of
// s3-checksum-residuals-and-record-backlog.md 1.4.3 (part 1 is pat5242880,
// part 2 pat1048576): the hypothesis the research records until
// record-backlog item R08 runs. The CRC32 example of
// sigv4-checksums-endpoints.md 4.4 is a checksum.Composite unit vector
// (internal/checksum TestCompositeVectors): its first part is under the
// 5 MiB a Complete requires.
func TestCompositeChecksumVectors(t *testing.T) {
	pattern := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i % 251)
		}
		return b
	}
	p1, p2 := pattern(minPart), pattern(1<<20)
	for alg, want := range map[checksum.Algorithm]string{
		checksum.XXHASH64:  "9GhH8zktinM=-2",
		checksum.XXHASH3:   "f6EQWtiO3o8=-2",
		checksum.XXHASH128: "Ger1rESaX/PiNtq26MVKfQ==-2",
		checksum.MD5:       "TcMA1YYQbcbHRS4dun66LA==-2",
		checksum.SHA512:    "hbWfVq2dROxQPWBMcw1EZgLLOpNmP/zfonMOFFvvC1LaQbYma4kY9a3JmG7oMa9BgHj2grxxoS0u7FKeSlg7vQ==-2",
	} {
		runObjects(t, func(h *harness) {
			h.createBucket("vectors")
			id := h.startUpload("vectors", "k", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithm(alg.String()))})
			first := h.sendPart("vectors", "k", id, 1, p1, s3api.UploadPartInput{})
			second := h.sendPart("vectors", "k", id, 2, p2, s3api.UploadPartInput{})
			done, err := complete(h, "vectors", "k", id, []s3api.CompletedPart{listed(1, first), listed(2, second)}, s3api.CompleteMultipartUploadInput{})
			if err != nil {
				t.Fatalf("%s Complete: %s %s", alg, err.ID.QueryCode, err.Message)
			}
			got := reported(done.ChecksumCRC32, done.ChecksumCRC32C, done.ChecksumCRC64NVME, done.ChecksumSHA1, done.ChecksumSHA256,
				done.ChecksumSHA512, done.ChecksumMD5, done.ChecksumXXHASH64, done.ChecksumXXHASH3, done.ChecksumXXHASH128)
			if got[alg] != want {
				t.Errorf("%s COMPOSITE = %q, want %q", alg, got[alg], want)
			}
		})
	}
}
