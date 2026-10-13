package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func (h *Handler) faultList(w http.ResponseWriter, r *http.Request, _ []string) {
	active := h.o.Faults.List()
	out := admin.FaultList{Faults: make([]admin.Fault, 0, len(active))}
	for i := range active {
		f, err := h.wireFault(active[i].Rule, active[i].Hits)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out.Faults = append(out.Faults, f)
	}
	h.ok(w, r, out)
}

func (h *Handler) faultClear(w http.ResponseWriter, _ *http.Request, _ []string) {
	h.o.Faults.Clear()
	h.noContent(w)
}

// faultPut adds the rule in the body under the id in the path, replacing an
// active rule with that id. A rule the store refuses answers invalid_request
// with the field at fault and, for an error shape the operation does not
// send, the shapes it does.
func (h *Handler) faultPut(w http.ResponseWriter, r *http.Request, args []string) {
	id := args[0]
	data, aerr := readBody(w, r, "fault rule")
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	var rule fault.Rule
	if err := json.Unmarshal(data, &rule); err != nil {
		h.fail(w, r, ruleParseError(err))
		return
	}
	if rule.ID != "" && string(rule.ID) != id {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "id", Message: fmt.Sprintf("the body names rule %q but the path names %q", rule.ID, id), Hint: "omit id from the body or make the two agree"})
		return
	}
	rule.ID = ref.RuleID(id)
	if _, rerr := h.o.Faults.Add(rule); rerr != nil {
		known := make([]string, len(rerr.Known))
		for i, s := range rerr.Known {
			known[i] = string(s)
		}
		h.fail(w, r, &admin.Error{
			Code:  admin.InvalidRequest,
			Field: rerr.Field,
			// The caller chose the id in the path, and the CLI mints one
			// for a rule given without, so the message leaves it out.
			// The field leads the reason, which reads as its predicate;
			// admin.Error.Error, the CLI's text, does not render Field.
			Message: "fault rule rejected, no rule was added or changed: " + rerr.Field + ": " + rerr.Reason,
			Known:   known,
		})
		return
	}
	f, aerr := h.wireFault(rule, 0)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	h.ok(w, r, f)
}

// ruleParseError is the refusal of a fault rule body that does not parse.
// An operation not in service.Operation form is named on its own, without
// the decoder's account of Go types the client never sees.
func ruleParseError(err error) *admin.Error {
	out := &admin.Error{
		Code:    admin.InvalidRequest,
		Message: "the fault rule does not parse: " + err.Error(),
		Hint:    `'understudy faults add --help' lists a rule's parts; as JSON a rule reads {"phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}`,
	}
	if pe, ok := errors.AsType[*fault.RuleParseError](err); ok {
		out.Field = pe.Path
	}
	if op, ok := errors.AsType[*fault.OperationNameError](err); ok {
		out.Field = "match.operations"
		out.Message = fmt.Sprintf("the fault rule does not parse: %q is not an operation; the form is service.Operation, such as sqs.SendMessage", op.Text)
	}
	return out
}

func (h *Handler) faultDelete(w http.ResponseWriter, r *http.Request, args []string) {
	if !h.o.Faults.Remove(ref.RuleID(args[0])) {
		h.fail(w, r, &admin.Error{Code: admin.NotFound, Message: fmt.Sprintf("no active fault rule has id %q", args[0]), Hint: "list the active rules with 'understudy faults list' (GET " + admin.Prefix + "/faults); a next-n rule leaves after its last hit"})
		return
	}
	h.noContent(w)
}

// wireFault renders an active rule in the public form: the rule's own JSON
// read back as an admin.FaultRule, so the two forms cannot drift.
func (h *Handler) wireFault(rule fault.Rule, hits int) (admin.Fault, *admin.Error) {
	data, err := rule.MarshalJSON()
	if err != nil {
		return admin.Fault{}, internal("encoding fault rule "+string(rule.ID), err)
	}
	var out admin.FaultRule
	if err := json.Unmarshal(data, &out); err != nil {
		return admin.Fault{}, internal("reading fault rule "+string(rule.ID)+" as its public form", err)
	}
	warning, _ := h.o.Faults.RetryableWarning(rule)
	return admin.Fault{Rule: out, Hits: hits, Warning: warning}, nil
}
