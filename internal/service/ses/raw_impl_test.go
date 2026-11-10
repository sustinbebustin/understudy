package ses_test

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// runVerifiedWithBlobs is runVerified with the blob store in reach, built
// as runWith builds it.
func runVerifiedWithBlobs(t *testing.T, fn func(h *harness, blobs *blob.Store)) {
	t.Helper()
	o := ses.Options{VerifiedIdentities: ses.VerifiedIdentities{Addresses: []string{sender}, Domains: []string{verifiedDomain}}}
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 1<<20)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		d := k.Deps(sesapi.Service())
		d.Blob = blobs
		svc := ses.New(d, o)
		d2 := k.Deps(sesv2api.Service())
		d2.Blob = blobs
		h := &harness{K: k, svc: svc, v2: ses.NewV2(d2, svc)}
		h.verifyEmail(sender)
		h.verifyDomain(verifiedDomain)
		fn(h, blobs)
	}, kerneltest.Services(sesapi.Service(), sesv2api.Service()))
}

// The raw bytes live in the blob store: the snapshot section names the
// blob, a refused send stores none, and clearing the message releases it.
func TestRawMessageBlobLifecycle(t *testing.T) {
	runVerifiedWithBlobs(t, func(h *harness, blobs *blob.Store) {
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(mime("body\r\n", "From: stranger@unverified.test", "To: "+simulator)))
		if err == nil {
			h.Fatal("an unverified raw sender was accepted")
		}
		if refs := blobs.Refs(); len(refs) != 0 {
			h.Fatalf("a refused send left blobs %v", refs)
		}

		data := mime("body\r\n", "From: "+sender, "To: "+simulator, "Subject: s")
		must(h, sesapi.OpSendRawEmail, rawEmail(data))
		sum := sha256.Sum256(data)
		want := hex.EncodeToString(sum[:])
		sections, eerr := h.svc.Export()
		if eerr != nil {
			h.Fatal(eerr)
		}
		if len(sections) != 1 || !slices.Equal(sections[0].Blobs, []string{want}) {
			h.Fatalf("exported sections %+v, want one naming blob %s", sections, want)
		}
		ref, perr := blob.ParseRef(want)
		if perr != nil {
			h.Fatal(perr)
		}
		if _, serr := blobs.Size(ref); serr != nil {
			h.Fatalf("the raw message's blob is missing after the send: %v", serr)
		}
		if n, cerr := h.svc.ClearMessages(ses.MessageQuery{}); cerr != nil || n == 0 {
			h.Fatalf("ClearMessages = %d, %v", n, cerr)
		}
		if _, serr := blobs.Size(ref); serr == nil {
			h.Error("the raw message's blob outlived its cleared record")
		}
	})
}

// Without Source, the From and Sender headers are senders; Return-Path is
// one either way (ses.md 4.2, 6.1).
func TestRawSenderHeadersAreChecked(t *testing.T) {
	const stranger = "stranger@unverified.test"
	runVerified(t, ses.Options{}, func(h *harness) {
		for _, header := range []string{"Sender: " + stranger, "Return-Path: <" + stranger + ">"} {
			data := mime("body\r\n", "From: "+sender, "To: "+simulator, header)
			_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
			want := stranger
			if header[0] == 'R' {
				want = "<" + stranger + ">"
			}
			wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, want))
		}
	})
}

// A given Source is the sender, so an unverified From header does not
// fail the check, and the record's From is Source's addr-spec (ses.md 4.2).
func TestRawSourceTakesPrecedenceOverFrom(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := rawEmail(mime("body\r\n", "From: stranger@unverified.test", "To: "+simulator))
		in.Source = new("Sender <" + sender + ">")
		must(h, sesapi.OpSendRawEmail, in)
		m := h.sends()[0]
		if m.Source != *in.Source || !slices.Equal(m.From, []string{sender}) {
			h.Errorf("Source %q From %q, want %q and [%s]", m.Source, m.From, *in.Source, sender)
		}
	})
}

// A header block that does not parse, and a multipart body without its
// closing boundary, answer RawUnparseableText.
func TestRawUnparseableMessage(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for name, data := range map[string][]byte{
			"header": []byte("this is not a header line\r\n\r\nbody\r\n"),
			"multipart": mime("--b\r\nContent-Type: text/plain\r\n\r\npart",
				"From: "+sender, "To: "+simulator, `Content-Type: multipart/mixed; boundary="b"`),
		} {
			_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
			if err == nil {
				h.Errorf("%s: accepted", name)
				continue
			}
			wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.RawUnparseableText)
		}
	})
}

// Under union a recipient both in Destinations and in a header counts
// once; header recipients keep their display names as written.
func TestRawUnionEnvelopeCountsEachMailboxOnce(t *testing.T) {
	runVerified(t, ses.Options{RawEnvelope: ses.RawEnvelopeUnion}, func(h *harness) {
		named := `"Rec Ipient" <` + recipient + ">"
		data := mime("body\r\n", "From: "+sender, "To: "+simulator+", Rec Ipient <"+recipient+">")
		must(h, sesapi.OpSendRawEmail, rawEmail(data, simulator))
		m := h.sends()[0]
		if !slices.Equal(m.Envelope, []string{simulator, named}) || !slices.Equal(m.To, []string{simulator, named}) {
			h.Errorf("envelope %q to %q, want [%s %s] for both", m.Envelope, m.To, simulator, named)
		}
		if got := h.quota().SentLast24Hours; got != 1 {
			h.Errorf("SentLast24Hours = %v, want 1 (the simulator is not counted)", got)
		}
	})
}
