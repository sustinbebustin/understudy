package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ServiceAuth is a service's own auth vocabulary and its answer to
// credentials auth.Parse cannot read, for a service whose protocol is in no
// awserr family (S3). server/wire.go builds it from the service's package,
// which the edge may not import.
type ServiceAuth struct {
	Vocabulary auth.Vocabulary
	// ParseError renders a parse failure; S3 answers some with identities
	// no Vocabulary slot holds (AuthorizationQueryParametersError,
	// InvalidArgument).
	ParseError func(auth.ParseError) *awserr.Error
	// Payload says who checks a SigV4-signed body of an operation that
	// streams its input under sigv4 verify; the edge hashes every other.
	Payload PayloadCheck
}

// PayloadCheck says who checks a SigV4-signed request's body against the
// payload hash the request sent.
type PayloadCheck uint8

// PayloadCheck values.
const (
	// EdgeHashesPayload: the edge reads the body whole, within the
	// operation's cap, and verifies the signature over its hash. It is the
	// zero value and the check of a service without a ServiceAuth entry.
	EdgeHashesPayload PayloadCheck = iota
	// ServiceChecksPayload: for an operation whose input streams a blob
	// (S3 PutObject and UploadPart, whose bodies are too large to buffer),
	// the edge verifies the signature over the hash the request sent and
	// leaves the body unread, and the service checks the body against that
	// hash as it streams it. Every other operation of the service is
	// hashed by the edge, as under EdgeHashesPayload, so its body is bound
	// to the signature.
	ServiceChecksPayload
)

// streamsInput reports whether op's input has a streaming blob member,
// the body a service reads as a stream rather than through its codec.
func streamsInput(op *registry.Operation) bool {
	if op.Input == nil {
		return false
	}
	for _, m := range op.Input.Members {
		if m.Target != nil && m.Target.Streaming && m.Target.Type == smithy.TypeBlob {
			return true
		}
	}
	return false
}

// vocabulary is the auth vocabulary of op spoken over protocol p: the
// operation's own when Options.OperationAuth has one, else its service's
// when Options.ServiceAuth has one, else p's family's, else the JSON
// family's.
func (h *Handler) vocabulary(op *registry.Operation, p ref.ProtocolID) auth.Vocabulary {
	if v, ok := h.opts.OperationAuth[op.ID]; ok {
		return v
	}
	if sa, ok := h.opts.ServiceAuth[op.Service.ID]; ok {
		return sa.Vocabulary
	}
	family, ok := awserr.FamilyOf(p)
	if !ok {
		family = awserr.JSONFamily
	}
	v, _ := auth.VocabularyFor(family)
	return v
}

// authError renders credentials that cannot be read in op's words over p.
func (h *Handler) authError(e auth.ParseError, op *registry.Operation, p ref.ProtocolID) *awserr.Error {
	if _, own := h.opts.OperationAuth[op.ID]; !own {
		if sa, ok := h.opts.ServiceAuth[op.Service.ID]; ok && sa.ParseError != nil {
			return sa.ParseError(e)
		}
	}
	return e.AWSError(h.vocabulary(op, p))
}

// verifyTarget is what verification needs of a claimed request: the
// request as the client signed it, the claimed form whose path is S3's
// path-style resource for SigV2 and whose body the codec reads, the
// operation, the protocol whose vocabulary answers and the codec whose
// BodyLimit caps the body.
type verifyTarget struct {
	signed  *http.Request
	claimed *http.Request
	op      *registry.Operation
	p       ref.ProtocolID
	codec   Codec
}

// caller is who a request's credentials name: the principal and how the
// identity store resolved the key, nil when no key was sent or there is no
// store. The edge resolves a request's key once, so the secret its
// signature is verified with and the principal it runs as come from one
// lookup.
type caller struct {
	principal identity.Principal
	res       identity.Resolution
}

// resolve resolves the access key (ADR 0012). A real-looking key
// understudy did not mint is read as the default account, with one warning
// per key.
func (h *Handler) resolve(ctx context.Context, a auth.Auth) caller {
	key, ok := auth.AccessKey(a)
	switch {
	case !ok && a != nil:
		return caller{principal: identity.Anonymous{}}
	case h.opts.Identity == nil || a == nil:
		return caller{principal: identity.Root{Account: aws.DefaultAccount()}}
	}
	p, res := h.opts.Identity.Resolve(key)
	if fb, ok := res.(identity.Fallback); ok && fb.Warn {
		h.log.LogAttrs(ctx, slog.LevelWarn, "an AWS access key understudy did not mint is read as the default account, never decoded into its real account",
			slog.String("account", aws.DefaultAccount().String()))
	}
	return caller{principal: p, res: res}
}

// verify is pipeline step 4a (ARCHITECTURE.md 4, 7): under sigv4 verify it
// checks a claimed request's credentials, which resolved as res, and
// returns them once they verify, or the refusal in the claimed service's
// dialect. Anonymous requests and every request under sigv4 parse pass
// unverified (nil, nil).
//
// The checks run in order: temporary credentials must send their session
// token and be unexpired on the wall clock (STS stamps Expiration in wall
// time), the key must have a secret (a key understudy minted, a static
// key, the dummy key or a 12-digit key; ADR 0012 pre-tag review
// amendment), then the signature must verify. Request skew is judged on the wall
// clock, presigned expiry on the emulator clock: a SigV4 URL's lifetime
// counts from its X-Amz-Date placed on the emulator timeline (h.timeline),
// a SigV2 URL's Expires is read as an emulator instant. The body is read
// whole, within the operation's cap, and hashed, and the codec then reads
// the same bytes, unless the operation streams its input and its service
// checks the payload itself (ServiceChecksPayload): then the body stays
// unread, its signed x-amz-content-sha256 standing for it until the
// service checks it at EOF.
func (h *Handler) verify(t verifyTarget, a auth.Auth, res identity.Resolution) (auth.Auth, *awserr.Error) {
	key, signed := auth.AccessKey(a)
	if !h.opts.Verify || !signed {
		return nil, nil
	}
	v := h.vocabulary(t.op, t.p)
	clk := h.opts.Kernel.Clock()
	if err := sessionRefusal(t.signed, res, v, clk.Wall()); err != nil {
		return nil, err
	}
	sk, ok := identity.SecretOf(res)
	if !ok {
		return nil, auth.UnknownKey(key, v)
	}
	secret := string(sk)
	var err *awserr.Error
	switch a := a.(type) {
	case auth.SigV4Header:
		body, berr := h.verifiedBody(t, a.PayloadHash)
		if berr != nil {
			return nil, berr
		}
		err = auth.Verify(a, auth.Normalize(t.signed, body), secret, clk.Wall(), v)
	case auth.SigV4Query:
		body, berr := h.verifiedBody(t, a.PayloadHash)
		if berr != nil {
			return nil, berr
		}
		err = auth.VerifyPresignedSince(a, auth.Normalize(t.signed, body), secret, clk.Wall(), h.timeline.at(clk, a.Time), clk.Now(), v)
	case auth.SigV2Header:
		err = auth.VerifySigV2(a, auth.Normalize(t.signed, auth.Unread{}), requestPath(t.claimed), secret, clk.Wall(), v)
	case auth.SigV2Query:
		err = auth.VerifySigV2Presigned(a, auth.Normalize(t.signed, auth.Unread{}), requestPath(t.claimed), secret, clk.Now(), v)
	case auth.Anonymous:
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// sessionRefusal refuses temporary credentials whose request does not send
// their session token, or that expired by wall; nil for any other key.
func sessionRefusal(signed *http.Request, res identity.Resolution, v auth.Vocabulary, wall time.Time) *awserr.Error {
	minted, ok := res.(identity.Minted)
	if !ok {
		return nil
	}
	temp, ok := minted.Lifetime.(identity.Temporary)
	if !ok {
		return nil
	}
	token, _ := auth.SessionToken(signed)
	if subtle.ConstantTimeCompare([]byte(token), []byte(temp.SessionToken)) != 1 {
		return auth.InvalidToken(v)
	}
	if !wall.Before(temp.Expiry) {
		return auth.ExpiredToken(v)
	}
	return nil
}

// verifiedBody is what the SigV4 verifier knows of the body: Unread for a
// request that sent its payload hash to an operation that streams its
// input to a service that checks it (ServiceChecksPayload), else the hash
// of the whole body, which it reads within the cap and leaves for the codec
// to read again.
func (h *Handler) verifiedBody(t verifyTarget, sent auth.PayloadHash) (auth.Body, *awserr.Error) {
	_, notSent := sent.(auth.NotSent)
	if h.opts.ServiceAuth[t.op.Service.ID].Payload == ServiceChecksPayload && streamsInput(t.op) && !notSent {
		return auth.Unread{}, nil
	}
	var body []byte
	if t.signed.Body != nil {
		limit := t.codec.BodyLimit(t.op)
		if t.signed.ContentLength > limit {
			return nil, tooLarge(t.op.ID, limit)
		}
		read, err := io.ReadAll(&cappedReader{rc: t.signed.Body, limit: limit, op: t.op.ID})
		if err != nil {
			if boundary, ok := errors.AsType[*awserr.BoundaryError](err); ok && boundary.Err != nil {
				return nil, boundary.Err
			}
			return nil, awserr.RequestAborted()
		}
		body = read
	}
	t.signed.Body = io.NopCloser(bytes.NewReader(body))
	t.claimed.Body = io.NopCloser(bytes.NewReader(body))
	return auth.Hashed{SHA256: sha256.Sum256(body)}, nil
}
