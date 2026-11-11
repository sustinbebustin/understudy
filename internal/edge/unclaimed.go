package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// unknownOperation is svc's answer for a request of protocol p naming no
// operation of it: the descriptor's UnknownOperationError for the protocol
// family, else the generic one. Coral sends the JSON family's without a
// message (error-matrix.md 5.5 T5); the Query family's names the action.
func (h *Handler) unknownOperation(svc *registry.Service, p ref.ProtocolID, name string) *awserr.Error {
	family, ok := awserr.FamilyOf(p)
	if !ok {
		family = awserr.JSONFamily
	}
	var id awserr.Identity
	found := false
	if svc.UnknownOperationError != nil {
		id, found = svc.UnknownOperationError(family)
	}
	if !found {
		id = awserr.UnknownOperationException()
		if family == awserr.QueryFamily {
			id = awserr.InvalidAction()
		}
	}
	e := &awserr.Error{ID: id, Origin: awserr.Generic{}}
	if family == awserr.QueryFamily {
		e.Message = "The action " + name + " is not valid for this endpoint."
	}
	return e
}

// unknownOperationXML is Coral's answer to a request that reaches a JSON
// service with another Content-Type or method: 404 with this body and no
// Content-Type (error-matrix.md 5.5 T7).
const unknownOperationXML = "<UnknownOperationException/>\n"

// answerForService answers a request signed for svc that no protocol rule
// claimed, as svc's own front end does (ADR 0013): a POST with no body, or
// a body in a JSON media type svc speaks, is an unknown operation in that
// JSON envelope; anything else, the other awsJson version included, is
// Coral's 404. The request is journaled as Unclaimed with every rule's
// reason.
func (h *Handler) answerForService(ctx context.Context, x *exchange, svc *registry.Service, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	mt := mediaType(x.r.Header.Get("Content-Type"))
	if x.r.Method == http.MethodPost {
		for _, p := range svc.Protocols {
			if family, ok := awserr.FamilyOf(p); !ok || family != awserr.JSONFamily {
				continue
			}
			if x.r.ContentLength != 0 && mt != "application/json" && mt != jsonMediaType(p) {
				continue
			}
			if codec, ok := h.opts.Codecs[p]; ok {
				h.write(ctx, x, codec.EncodeError(x.r, svc, string(x.id), h.unknownOperation(svc, p, "")))
				return
			}
		}
	}
	x.w.Header()["Content-Type"] = nil
	h.write(ctx, x, Response{Status: http.StatusNotFound, Body: strings.NewReader(unknownOperationXML)})
}

// jsonMediaType is the media type of awsJson protocol p, empty for any
// other protocol. A JSON 1.0 service answers a 1.1 body with the 404 above
// (error-matrix.md 5.5 T7, dynalite connection.js).
func jsonMediaType(p ref.ProtocolID) string {
	switch p {
	case ref.AWSJSON10:
		return "application/x-amz-json-1.0"
	case ref.AWSJSON11:
		return "application/x-amz-json-1.1"
	case ref.AWSQuery, ref.RESTJSON1, ref.RESTXML, ref.RPCv2CBOR, ref.Raw:
	}
	return ""
}

// legacyQuery answers a legacy Query request (rule 8) with 501
// NotImplemented in the service's JSON envelope, naming the protocol, the
// action and the X-Amz-Target that reaches the same operation, and
// journals it as Unclaimed with the rule trace.
func (h *Handler) legacyQuery(ctx context.Context, x *exchange, rt legacyQuery) {
	h.journalUnclaimed(ctx, x, rt.reasons)
	form, target := "?Action="+rt.action, rt.svc.JSONTargetPrefix+"."+rt.action
	if rt.action == "" {
		form, target = "a form body", rt.svc.JSONTargetPrefix+".<Action>"
	}
	e := &awserr.Error{
		ID: awserr.NotImplemented(),
		Message: "understudy does not serve the legacy Query protocol (" + form + " without X-Amz-Target) for " + string(rt.svc.ID) +
			", which AWS also accepts. Send the JSON 1.1 request instead, with X-Amz-Target: " + target +
			", as the AWS SDKs and CLI do. Nothing was changed.",
		Origin: awserr.Generic{},
	}
	for _, p := range rt.svc.Protocols {
		if family, ok := awserr.FamilyOf(p); !ok || family != awserr.JSONFamily {
			continue
		}
		if codec, ok := h.opts.Codecs[p]; ok {
			h.write(ctx, x, codec.EncodeError(x.r, rt.svc, string(x.id), e))
			return
		}
	}
	h.answerUnrouted(ctx, x, e)
}

// notEmulated answers a request signed for a service understudy does not
// emulate (ec2, rds, firehose, ...) with 501 NotImplemented, outside every
// SDK's retry set, naming the service and the ones understudy emulates.
// The rule trace stays in the Unclaimed journal event: the client needs
// only the one fact.
func (h *Handler) notEmulated(ctx context.Context, x *exchange, rt notEmulated) {
	h.journalUnclaimed(ctx, x, rt.reasons)
	if h.warned.first("not emulated " + rt.signingName) {
		h.log.LogAttrs(ctx, slog.LevelWarn, "a client called a service understudy does not emulate",
			slog.String("signing_name", rt.signingName), slog.String("method", x.r.Method),
			slog.String("path", requestPath(x.r)), slog.String("request_id", string(x.id)))
	}
	h.answerUnrouted(ctx, x, &awserr.Error{
		ID: awserr.NotImplemented(),
		Message: "This request is signed for the AWS service " + rt.signingName + ", which understudy does not emulate. understudy emulates " +
			h.emulated + "; run 'understudy coverage' for the operations of each. Nothing was changed.",
		Origin: awserr.Generic{},
	})
}

// misscoped answers a request whose credential is scoped to a service name
// AWS refuses for the operation it names, such as DynamoDB Streams scoped
// to streams.dynamodb, with AWS's 400 InvalidSignatureException in the
// service's envelope (refusedScope). The service never sees it, so the
// rule trace is journaled as Unclaimed; the client gets AWS's text, which
// names the scope to sign for.
func (h *Handler) misscoped(ctx context.Context, x *exchange, rt misscoped) {
	h.journalUnclaimed(ctx, x, rt.reasons)
	x.protocol = rt.codec.Protocol()
	h.write(ctx, x, rt.codec.EncodeError(x.r, rt.svc, string(x.id), &awserr.Error{
		ID:      awserr.InvalidSignatureException(),
		Message: "Credential should be scoped to correct service: '" + rt.svc.SigningName + "'. ",
		Origin:  awserr.Generic{},
	}))
}

// unclaimed answers a request no rule claimed (rule 9): an AWS-shaped 400
// whose message lists why each rule passed it over, and the Unclaimed
// journal event, the one event the edge writes itself.
func (h *Handler) unclaimed(ctx context.Context, x *exchange, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	texts := make([]string, len(reasons))
	for i, r := range reasons {
		texts[i] = r.text
	}
	last := ""
	if len(texts) > 0 {
		last = texts[len(texts)-1]
	}
	if h.warned.first("unclaimed " + x.r.Method + " " + last) {
		h.log.LogAttrs(ctx, slog.LevelWarn, "understudy could not route a request to a service",
			slog.String("method", x.r.Method), slog.String("host", x.r.Host), slog.String("path", requestPath(x.r)),
			slog.String("reason", last), slog.String("request_id", string(x.id)))
	}
	h.answerUnrouted(ctx, x, &awserr.Error{
		ID:      awserr.UnknownOperationException(),
		Message: "understudy could not route this request to a service: " + strings.Join(texts, "; "),
		Origin:  awserr.Generic{},
	})
}

// localStack answers a path under LocalStack's reserved prefix, such as a
// migrated health check's /_localstack/health, with 404 in the admin API's
// JSON error envelope naming understudy's own health check, and journals
// it as Unclaimed. S3 would otherwise answer NoSuchBucket for a bucket no
// name can be, which points nowhere.
func (h *Handler) localStack(ctx context.Context, x *exchange, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	path := requestPath(x.r)
	if h.warned.first("localstack " + path) {
		h.log.LogAttrs(ctx, slog.LevelWarn, "a client called a LocalStack endpoint, which understudy does not serve",
			slog.String("method", x.r.Method), slog.String("path", path), slog.String("request_id", string(x.id)))
	}
	e := &admin.Error{
		Code: admin.NotFound,
		Message: path + " is a LocalStack endpoint; understudy does not serve LocalStack's /" + localStackSegment +
			"/ API. Nothing was changed.",
		Hint: "check that understudy is ready with GET " + admin.Prefix + "/health, or run 'understudy health --wait 30s'",
	}
	body, err := json.Marshal(admin.Envelope{Error: e})
	if err != nil {
		h.write(ctx, x, plainError(http.StatusNotFound, e.Message+" "+e.Hint))
		return
	}
	h.write(ctx, x, Response{
		Status: http.StatusNotFound,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   bytes.NewReader(append(body, '\n')),
	})
}

// executeAPIBody is the answer to an execute-api request until understudy
// implements API Gateway invoke: API Gateway's own {"message": ...} shape
// (apigateway-http.md 4.5), naming what is missing and where to ask for it.
const executeAPIBody = `{"message":"understudy does not implement API Gateway invoke (the execute-api data plane); nothing was changed. To ask for it, see https://github.com/sustinbebustin/understudy/issues?q=execute-api"}`

// executeAPI answers a request to API Gateway's execute-api data plane,
// which understudy does not implement, with 501 NotImplemented (ADR 0003),
// and journals it as Unclaimed. Whether AWS sends a Content-Type on the
// gateway's own answers is unverified (apigateway-http.md 4.5, APIGW2-06);
// application/json names the body.
func (h *Handler) executeAPI(ctx context.Context, x *exchange, reasons []reason) {
	h.journalUnclaimed(ctx, x, reasons)
	if h.warned.first("execute-api") {
		h.log.LogAttrs(ctx, slog.LevelWarn, "a client called API Gateway invoke (execute-api), which understudy does not implement",
			slog.String("method", x.r.Method), slog.String("host", x.r.Host), slog.String("path", requestPath(x.r)),
			slog.String("request_id", string(x.id)))
	}
	h.write(ctx, x, Response{
		Status: http.StatusNotImplemented,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   strings.NewReader(executeAPIBody),
	})
}

// executeAPIClaimedTwice answers an execute-api request whose API id more
// than one front holds with 500 in API Gateway's {"message": ...} shape,
// naming the services (ADR 0019 amendment), journals it as Unclaimed
// with that reason, and logs it as the bug it is. Neither front runs.
func (h *Handler) executeAPIClaimedTwice(ctx context.Context, x *exchange, rt executeAPIClaimedTwice) {
	h.journalUnclaimed(ctx, x, rt.reasons)
	h.log.LogAttrs(ctx, slog.LevelError, "an execute-api API id is held by more than one API Gateway service",
		slog.String("reason", rt.message), slog.String("host", x.r.Host), slog.String("path", requestPath(x.r)),
		slog.String("request_id", string(x.id)))
	body, err := json.Marshal(struct {
		Message string `json:"message"`
	}{rt.message})
	if err != nil {
		h.write(ctx, x, plainError(http.StatusInternalServerError, rt.message))
		return
	}
	h.write(ctx, x, Response{
		Status: http.StatusInternalServerError,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   bytes.NewReader(body),
	})
}

// answerUnrouted renders e, the answer to a request no service claimed, in
// the envelope its client parses: that of the protocol the request's shape
// names (answerProtocol), else Options.Unclaimed's, else plain text.
func (h *Handler) answerUnrouted(ctx context.Context, x *exchange, e *awserr.Error) {
	for _, p := range []ref.ProtocolID{answerProtocol(x.r), h.opts.Unclaimed} {
		if codec, ok := h.opts.Codecs[p]; ok {
			h.write(ctx, x, codec.EncodeError(x.r, nil, string(x.id), e))
			return
		}
	}
	h.write(ctx, x, plainError(e.ID.Status, e.Message))
}

// answerProtocol is the protocol a request no service claimed was sent in,
// judged by its shape alone: rpcv2Cbor by its Smithy-Protocol header,
// awsJson by X-Amz-Target and its media type, awsQuery by a form body or a
// GET with Action, restJson1 by a JSON body, and restXml for any other
// request. A JSON client keeps an XML body it cannot parse as the error's
// message, while an XML client fails on JSON, so XML is the safer guess.
func answerProtocol(r *http.Request) ref.ProtocolID {
	mt := mediaType(r.Header.Get("Content-Type"))
	switch {
	case smithyProtocolHeader(r):
		return ref.RPCv2CBOR
	case r.Header.Get("X-Amz-Target") != "" && mt == jsonMediaType(ref.AWSJSON11):
		return ref.AWSJSON11
	case r.Header.Get("X-Amz-Target") != "":
		return ref.AWSJSON10
	case queryShaped(r):
		return ref.AWSQuery
	case mt == "application/json":
		return ref.RESTJSON1
	}
	return ref.RESTXML
}

func (h *Handler) journalUnclaimed(ctx context.Context, x *exchange, reasons []reason) {
	_, err := h.opts.Kernel.Journal().Append(journal.Event{
		At:     x.admitted,
		Detail: journal.Unclaimed{Method: x.r.Method, Host: x.r.Host, Path: requestPath(x.r), Reasons: journaled(reasons)},
	})
	if err != nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "journal refused an unclaimed request",
			slog.String("request_id", string(x.id)), slog.String("error", err.Error()))
	}
}
