package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

func faultsCmd() *command {
	return group("faults", "List, add and remove fault-injection rules",
		leaf("list", "List the active rules in evaluation order", "faults list",
			[]string{"understudy faults list", "understudy faults list --output json"},
			func(*flag.FlagSet) action { return runFaultsList }),
		leaf("add", "Add a rule, or replace the rule with its id",
			"faults add [--id ID] [--service S] [--operation O] [--resource GLOB] [--account A] [--region R] [--origin any|external|internal] [--phase request|delivery] "+
				"(--error SHAPE | --throttle | --latency D | --after-commit SHAPE | --status-ok SHAPE | --drop) [--next N | --probability P]",
			[]string{
				"understudy faults add --operation sqs.SendMessage --throttle --next 3",
				"understudy faults add --id slow-s3 --service s3 --latency 2s",
				"understudy faults add --operation sqs.ReceiveMessage --error QueueDoesNotExist --probability 0.1",
				"understudy faults add --phase delivery --mechanism sns-to-sqs --drop --next 1",
			},
			faultsAdd),
		leaf("remove", "Remove the rule with an id", "faults remove ID",
			[]string{"understudy faults remove orders-throttle"},
			func(*flag.FlagSet) action {
				return func(ctx context.Context, r *runner, args []string) error {
					if err := wantArgs(args, 1, "one argument, the rule id"); err != nil {
						return err
					}
					return r.simple("removed fault rule "+args[0], func(c *admin.Client) error { return c.DeleteFault(ctx, args[0]) })
				}
			}),
		leaf("clear", "Remove every rule", "faults clear", []string{"understudy faults clear"},
			func(*flag.FlagSet) action {
				return func(ctx context.Context, r *runner, args []string) error {
					if err := wantArgs(args, 0, "no arguments"); err != nil {
						return err
					}
					return r.simple("removed every fault rule", func(c *admin.Client) error { return c.ClearFaults(ctx) })
				}
			}),
		leaf("load", "Add or replace the rules of a JSON file: a list of rules, or what 'faults list --output json' printed", "faults load FILE",
			[]string{"understudy faults load faults.json", "understudy faults list --output json > saved.json && understudy faults load saved.json"},
			func(*flag.FlagSet) action { return runFaultsLoad }),
	)
}

// simple runs a call that answers nothing and reports done.
func (r *runner) simple(done string, call func(*admin.Client) error) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	if err := call(c); err != nil {
		return err
	}
	return r.emit(struct {
		OK bool `json:"ok"`
	}{true}, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, done)
		return err
	})
}

func runFaultsList(ctx context.Context, r *runner, args []string) error {
	if err := wantArgs(args, 0, "no arguments"); err != nil {
		return err
	}
	c, err := r.client()
	if err != nil {
		return err
	}
	faults, err := c.Faults(ctx)
	if err != nil {
		return err
	}
	return r.emitFaults(admin.FaultList{Faults: faults}, faults)
}

// emitFaults prints v, whose rules are faults: JSON, where each rule
// carries its own warning, or a table on stdout of rows only, with the
// warnings on stderr. A warning's text names its rule.
func (r *runner) emitFaults(v any, faults []admin.Fault) error {
	if err := r.emit(v, func(w io.Writer) error { return writeFaults(w, faults) }); err != nil || r.output == outputJSON {
		return err
	}
	var warnings strings.Builder
	for i := range faults {
		if faults[i].Warning != "" {
			warnings.WriteString("warning: " + faults[i].Warning + "\n")
		}
	}
	_, err := io.WriteString(r.o.Stderr, warnings.String())
	return err
}

func writeFaults(w io.Writer, faults []admin.Fault) error {
	if len(faults) == 0 {
		_, err := fmt.Fprintln(w, "no fault rules")
		return err
	}
	tw := newTable(w)
	fmt.Fprintln(tw, "ID\tPHASE\tACTION\tTRIGGER\tHITS\tMATCH")
	for i := range faults {
		f := &faults[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", f.Rule.ID, f.Rule.Phase, describeAction(f.Rule.Action), describeTrigger(f.Rule.Trigger), f.Hits, describeMatch(&f.Rule.Match))
	}
	return tw.Flush()
}

func describeAction(a admin.FaultAction) string {
	switch a := a.(type) {
	case admin.InjectError:
		return "error " + a.Shape
	case admin.Throttle:
		return "throttle"
	case admin.Latency:
		return "latency " + a.Wall.String()
	case admin.AfterCommit:
		return "after-commit " + a.Shape
	case admin.StatusOKError:
		return "status-ok " + a.Shape
	case admin.DropDelivery:
		return "drop"
	}
	return "none"
}

func describeTrigger(t admin.FaultTrigger) string {
	switch t := t.(type) {
	case admin.Always:
		return "always"
	case admin.NextN:
		return "next " + strconv.Itoa(t.N)
	case admin.Probability:
		return "probability " + strconv.FormatFloat(t.P, 'g', -1, 64)
	}
	return "none"
}

func describeMatch(m *admin.FaultMatch) string {
	var parts []string
	add := func(name string, values []string) {
		if len(values) > 0 {
			parts = append(parts, name+"="+strings.Join(values, ","))
		}
	}
	add("service", m.Services)
	add("operation", m.Operations)
	add("mechanism", m.Mechanisms)
	add("account", m.Accounts)
	add("region", m.Regions)
	if m.Resource != "" {
		parts = append(parts, "resource="+m.Resource)
	}
	if m.Origin != "" && m.Origin != admin.AnyOrigin {
		parts = append(parts, "origin="+string(m.Origin))
	}
	if len(parts) == 0 {
		return "every call"
	}
	return strings.Join(parts, " ")
}

// faultFlags are the flags of faults add.
type faultFlags struct {
	id, resource, origin, phase              string
	errShape, message, afterCommit, statusOK string
	throttle, drop                           bool
	latency                                  time.Duration
	next                                     int
	probability                              float64
	services, operations, mechanisms         stringList
	accounts, regions                        stringList
	// set names the flags the command line gave, so a zero --next,
	// --probability or --latency is told from an absent one.
	set map[string]bool
}

func faultsAdd(fs *flag.FlagSet) action {
	var f faultFlags
	fs.StringVar(&f.id, "id", "", "rule id: letters, digits, '.', '_' and '-' (default a generated one)")
	fs.Var(&f.services, "service", "service to match, such as sqs; repeat for more")
	fs.Var(&f.operations, "operation", "operation to match as service.Operation, such as sqs.SendMessage; repeat for more")
	fs.Var(&f.mechanisms, "mechanism", "delivery path to match in the delivery phase, such as sns-to-sqs; repeat for more")
	fs.StringVar(&f.resource, "resource", "", "ARN glob the call must touch; * matches any run of characters")
	fs.Var(&f.accounts, "account", "account to match; repeat for more")
	fs.Var(&f.regions, "region", "region to match; repeat for more")
	fs.StringVar(&f.origin, "origin", "any", "calls to match by origin: any, external (from clients) or internal (between services)")
	fs.StringVar(&f.phase, "phase", "request", "hook point: request or delivery")
	fs.StringVar(&f.errShape, "error", "", "fail the call with this error shape, one the operation really sends")
	fs.StringVar(&f.message, "message", "", "message of the --error (default one naming the rule)")
	fs.BoolVar(&f.throttle, "throttle", false, "fail the call with the service's throttling error")
	durationVar(fs, &f.latency, "latency", 0, "delay the call by this much wall time")
	fs.StringVar(&f.afterCommit, "after-commit", "", "let the call take effect, then answer with this error shape")
	fs.StringVar(&f.statusOK, "status-ok", "", "answer 200 with this error shape in the body, as S3 does for some operations")
	fs.BoolVar(&f.drop, "drop", false, "discard a delivery attempt (delivery phase)")
	intVar(fs, &f.next, "next", 0, "fire on the next N matching calls, then remove the rule")
	fractionVar(fs, &f.probability, "probability", "fire on each matching call with this probability, between 0 and 1")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 0, "no arguments"); err != nil {
			return err
		}
		f.set = map[string]bool{}
		fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
		rule, err := f.rule()
		if err != nil {
			return err
		}
		c, err := r.client()
		if err != nil {
			return err
		}
		put, err := c.PutFault(ctx, rule)
		if err != nil {
			return err
		}
		return r.emitFaults(put, []admin.Fault{*put})
	}
}

// operationForm checks that each --operation is service.Operation, the
// only form a rule takes, before the server's JSON decoder refuses it.
func operationForm(ops []string) error {
	for _, op := range ops {
		svc, name, ok := strings.Cut(op, ".")
		if !ok || svc == "" || name == "" {
			return &usageError{msg: fmt.Sprintf("--operation %q is not service.Operation, such as sqs.SendMessage", op)}
		}
	}
	return nil
}

// rule builds the rule the flags describe; exactly one action is required
// and at most one of --next and --probability.
func (f *faultFlags) rule() (admin.FaultRule, error) {
	var actions []admin.FaultAction
	if f.errShape != "" {
		actions = append(actions, admin.InjectError{Shape: f.errShape, Message: f.message})
	}
	if f.throttle {
		actions = append(actions, admin.Throttle{})
	}
	if f.set["latency"] {
		if f.latency <= 0 {
			return admin.FaultRule{}, &usageError{msg: fmt.Sprintf("--latency %q is not a positive duration such as 250ms or 2s", f.latency)}
		}
		actions = append(actions, admin.Latency{Wall: f.latency})
	}
	if f.afterCommit != "" {
		actions = append(actions, admin.AfterCommit{Shape: f.afterCommit})
	}
	if f.statusOK != "" {
		actions = append(actions, admin.StatusOKError{Shape: f.statusOK})
	}
	if f.drop {
		actions = append(actions, admin.DropDelivery{})
	}
	if len(actions) != 1 {
		return admin.FaultRule{}, &usageError{msg: fmt.Sprintf("takes exactly one action of --error, --throttle, --latency, --after-commit, --status-ok and --drop; got %d", len(actions))}
	}
	if f.message != "" && f.errShape == "" {
		return admin.FaultRule{}, &usageError{msg: "--message goes with --error"}
	}
	if err := operationForm(f.operations); err != nil {
		return admin.FaultRule{}, err
	}
	if shaped := f.shapeFlag(); shaped != "" && len(f.operations) == 0 {
		return admin.FaultRule{}, &usageError{msg: shaped + " needs at least one --operation service.Operation, such as --operation sqs.SendMessage, since each operation sends its own errors"}
	}
	// The server refuses a --next below 1 and a --probability outside
	// (0, 1], so the values pass through as given.
	var trigger admin.FaultTrigger = admin.Always{}
	switch {
	case f.set["next"] && f.set["probability"]:
		return admin.FaultRule{}, &usageError{msg: "takes at most one of --next and --probability"}
	case f.set["next"]:
		trigger = admin.NextN{N: f.next}
	case f.set["probability"]:
		trigger = admin.Probability{P: f.probability}
	}
	id := f.id
	if id == "" {
		id = newRuleID()
	}
	return admin.FaultRule{
		ID:    id,
		Phase: admin.FaultPhase(f.phase),
		Match: admin.FaultMatch{
			Services: f.services, Operations: f.operations, Mechanisms: f.mechanisms,
			Resource: f.resource, Accounts: f.accounts, Regions: f.regions, Origin: admin.FaultOrigin(f.origin),
		},
		Action:  actions[0],
		Trigger: trigger,
	}, nil
}

// shapeFlag is the flag of the action that names an error shape, which the
// server resolves against the matched operations; empty for another
// action.
func (f *faultFlags) shapeFlag() string {
	switch {
	case f.errShape != "":
		return "--error"
	case f.afterCommit != "":
		return "--after-commit"
	case f.statusOK != "":
		return "--status-ok"
	}
	return ""
}

// newRuleID mints a rule id for a rule added without one.
func newRuleID() string {
	return "rule-" + strings.ToLower(rand.Text()[:10])
}

func runFaultsLoad(ctx context.Context, r *runner, args []string) error {
	if err := wantArgs(args, 1, "one argument, the rule file"); err != nil {
		return err
	}
	data, err := r.o.ReadFile(args[0])
	if err != nil {
		return &localError{op: "reading the fault rule file " + args[0], err: err, next: "check the path, then run 'understudy faults load FILE' again"}
	}
	rules, err := parseRules(data)
	if err != nil {
		return &usageError{msg: fmt.Sprintf("%s is neither a JSON list of fault rules nor what 'faults list --output json' prints: %v", args[0], err)}
	}
	c, err := r.client()
	if err != nil {
		return err
	}
	put := make([]admin.Fault, 0, len(rules))
	for i := range rules {
		rule := &rules[i]
		// A rule is named by its index, and by its id only when the file
		// gave one: a minted id means nothing to the user.
		name := fmt.Sprintf("rule %d", i)
		if rule.ID == "" {
			rule.ID = newRuleID()
		} else {
			name += fmt.Sprintf(" (%s)", rule.ID)
		}
		f, err := c.PutFault(ctx, *rule)
		if err != nil {
			return fmt.Errorf("%s of %s, after %d rule(s) were added: %w", name, args[0], i, err)
		}
		put = append(put, *f)
	}
	return r.emitFaults(admin.FaultList{Faults: put}, put)
}

// parseRules reads a JSON list of rules, or the {"faults": [{"rule": ...}]}
// document 'faults list --output json' prints, so a saved list loads back.
func parseRules(data []byte) ([]admin.FaultRule, error) {
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		var rules []admin.FaultRule
		err := json.Unmarshal(data, &rules)
		return rules, err
	}
	var list admin.FaultList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	rules := make([]admin.FaultRule, len(list.Faults))
	for i := range list.Faults {
		rules[i] = list.Faults[i].Rule
	}
	return rules, nil
}
