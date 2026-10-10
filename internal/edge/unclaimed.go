package edge

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// unknownOperation is svc's answer for a request of protocol p naming no
// operation of it: the overlay's unknownOperationError for the protocol
// family, else the generic one. Coral sends the JSON family's without a
// message (error-matrix.md 5.5 T5); the Query family's names the action.
func (h *Handler) unknownOperation(svc *registry.Service, p ref.ProtocolID, name string) *awserr.Error {
	family, ok := awserr.FamilyOf(p)
	if !ok {
		family = awserr.JSONFamily
	}
	var id awserr.Identity
	found := false
	if h.opts.UnknownOperationError != nil {
		id, found = h.opts.UnknownOperationError(svc.ID, family)
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
// claimed, as svc's own front end does (F7): a POST with no body, or a body
// in a JSON media type svc speaks, is an unknown operation in that JSON
// envelope; anything else, the other awsJson version included, is Coral's
// 404. The request is journaled as Unclaimed with every rule's reason.
func (h *Handler) answerForService(ctx context.Context, x *exchange, svc *registry.Service, reasons []string) {
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

// unclaimed answers a request no rule claimed (rule 9): an AWS-shaped 400
// whose message lists why each rule declined, and the Unclaimed journal
// event, the one event the edge writes itself.
func (h *Handler) unclaimed(ctx context.Context, x *exchange, reasons []string) {
	h.journalUnclaimed(ctx, x, reasons)
	e := &awserr.Error{
		ID:      awserr.UnknownOperationException(),
		Message: "understudy could not route this request to a service: " + strings.Join(reasons, "; "),
		Origin:  awserr.Generic{},
	}
	if codec, ok := h.opts.Codecs[h.opts.Unclaimed]; ok {
		h.write(ctx, x, codec.EncodeError(x.r, nil, string(x.id), e))
		return
	}
	h.write(ctx, x, plainError(http.StatusBadRequest, e.Message))
}

func (h *Handler) journalUnclaimed(ctx context.Context, x *exchange, reasons []string) {
	_, err := h.opts.Kernel.Journal().Append(journal.Event{
		At:     x.admitted,
		Detail: journal.Unclaimed{Method: x.r.Method, Host: x.r.Host, Path: requestPath(x.r), Reasons: reasons},
	})
	if err != nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "journal refused an unclaimed request",
			slog.String("request_id", string(x.id)), slog.String("error", err.Error()))
	}
}
