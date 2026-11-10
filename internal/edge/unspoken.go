package edge

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// kinesisCBOR is a Kinesis call in CBOR, the AWS SDK for Java's default
// wire form for Kinesis, which understudy does not serve (kinesis.md 2.3,
// 13.4 item 2). It is claimed by rule 6 so the client gets an answer it
// can parse, never a generic one.
type kinesisCBOR struct{ reasons []reason }

// http2Preface is an HTTP/2 prior-knowledge preface, which net/http hands
// to the handler as method PRI and target * (kinesis.md 2.4, 13.9).
type http2Preface struct{ reasons []reason }

func (kinesisCBOR) isRoute()  {}
func (http2Preface) isRoute() {}

// cborMediaType is the Content-Type of the AWS SDK for Java's CBOR
// protocol (kinesis.md 2.3).
const cborMediaType = "application/x-amz-cbor-1.1"

// kinesisTargetPrefix is the X-Amz-Target prefix of Kinesis operations.
const kinesisTargetPrefix = "Kinesis_20131202."

// kinesisCBORMessage names the client settings that make the Java SDK send
// JSON (kinesis.md 2.3: aws.cborEnabled and CBOR_ENABLED).
const kinesisCBORMessage = "understudy does not implement the CBOR protocol for Amazon Kinesis; it serves JSON (application/x-amz-json-1.1). " +
	"Set CBOR_ENABLED=false in the environment or -Daws.cborEnabled=false on the JVM so the AWS SDK for Java sends JSON. Nothing was changed."

// http2PrefaceMessage names the client setting that makes the Java SDK's
// async client speak HTTP/1.1 (kinesis.md 2.4).
const http2PrefaceMessage = "understudy speaks HTTP/1.1 only and received an HTTP/2 prior-knowledge preface (PRI * HTTP/2.0). Configure the client for HTTP/1.1: " +
	"with the AWS SDK for Java v2, build the async client with NettyNioAsyncHttpClient.builder().protocol(Protocol.HTTP1_1). Nothing was changed."

// isHTTP2Preface reports a request net/http made of an HTTP/2
// prior-knowledge preface.
func isHTTP2Preface(r *http.Request) bool { return r.Method == "PRI" && r.RequestURI == "*" }

// isKinesisCBOR reports a Kinesis call in CBOR: X-Amz-Target names a
// Kinesis operation and the Content-Type essence is CBOR's.
func isKinesisCBOR(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("X-Amz-Target"), kinesisTargetPrefix) && mediaType(r.Header.Get("Content-Type")) == cborMediaType
}

// answerKinesisCBOR answers 501 in CBOR, a map of text strings
// {"__type": "NotImplemented", "message": ...}, so the Java client parses
// it and shows the message, and journals the request as Unclaimed
// (localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.py:L775-L814 and
// tests/aws/services/kinesis/test_kinesis.snapshot.json:L230-L237).
func (h *Handler) answerKinesisCBOR(ctx context.Context, x *exchange, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	if h.warned.first("kinesis cbor") {
		h.log.LogAttrs(ctx, slog.LevelWarn, "a Kinesis client sent CBOR, which understudy does not serve; set CBOR_ENABLED=false or -Daws.cborEnabled=false",
			slog.String("target", x.r.Header.Get("X-Amz-Target")), slog.String("request_id", string(x.id)))
	}
	w := cbor.NewWriter()
	w.BeginMap()
	w.Key("__type")
	w.Text("NotImplemented")
	w.Key("message")
	w.Text(kinesisCBORMessage)
	w.EndMap()
	body, err := w.Bytes()
	if err != nil {
		// A map of two text strings always encodes.
		h.write(ctx, x, plainError(http.StatusNotImplemented, kinesisCBORMessage))
		return
	}
	h.write(ctx, x, Response{
		Status: http.StatusNotImplemented,
		Header: http.Header{"Content-Type": {cborMediaType}},
		Body:   bytes.NewReader(body),
	})
}

// answerHTTP2Preface answers 501 in plain text naming the HTTP/1.1 client
// setting. An HTTP/2 client cannot parse an HTTP/1.1 answer, so the
// Unclaimed journal event, whose reason carries the same text, is where
// the diagnostic is read (kinesis.md 13.9).
func (h *Handler) answerHTTP2Preface(ctx context.Context, x *exchange, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	if h.warned.first("http2 preface") {
		h.log.LogAttrs(ctx, slog.LevelWarn, "a client sent an HTTP/2 prior-knowledge preface; understudy speaks HTTP/1.1 only",
			slog.String("request_id", string(x.id)))
	}
	h.write(ctx, x, Response{
		Status: http.StatusNotImplemented,
		Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:   strings.NewReader(http2PrefaceMessage),
	})
}
