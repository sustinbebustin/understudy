package codegen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// IAMInput says where operations' IAM actions come from.
//
//sumtype:decl
type IAMInput interface{ isIAMInput() }

// ActionsTable is third_party/aws-service-reference/actions.json, the table
// derived from the AWS Service Reference (ADR 0016).
type ActionsTable struct{ Source Source }

// NoActions gives every modeled operation no IAM action: the synthetic
// protocol-test services, which no IAM policy names.
type NoActions struct{}

func (ActionsTable) isIAMInput() {}
func (NoActions) isIAMInput()    {}

// iamTable is the parsed actions table, per IAM service prefix.
type iamTable struct {
	byPrefix map[string]iamService
}

// iamService is one service's part of the table.
type iamService struct {
	// operations holds, per operation, the authorized actions in the
	// Service Reference's order.
	operations map[string][]registry.IAMAction
	// actions holds the names of every action the service lists under
	// Actions[], which may name an operation's own action that its
	// authorized list leaves out.
	actions map[string]bool
}

// iamEntry is one operation's row of the table.
type iamEntry struct {
	authorized []registry.IAMAction // in the table's order
	// listed says the service lists <prefix>:<operation> under Actions[].
	listed bool
}

// parseIAMTable reads actions.json. A NoActions input yields a nil table,
// whose lookups give every operation no action.
func parseIAMTable(in IAMInput) (*iamTable, error) {
	var src ActionsTable
	switch in := in.(type) {
	case ActionsTable:
		src = in
	case NoActions:
		return nil, nil //nolint:nilnil // NoActions has no table, which is not an error
	case nil:
		return nil, failf(StageLoad, "Inputs.IAM", "is unset; give an ActionsTable, or NoActions for a service no IAM policy names")
	}
	var v struct {
		Services []struct {
			Prefix     string `json:"prefix"`
			Operations []struct {
				Name       string   `json:"name"`
				Authorized []string `json:"authorized"`
			} `json:"operations"`
			Actions []struct {
				Name string `json:"name"`
			} `json:"actions"`
		} `json:"services"`
	}
	// The table's provenance fields are cmd/vendor's to check; unknown
	// properties are not an error here.
	if err := json.Unmarshal(src.Source.Content, &v); err != nil {
		return nil, failf(StageLoad, src.Source.Name, "not the derived Service Reference table: %w", err)
	}
	t := &iamTable{byPrefix: map[string]iamService{}}
	for _, s := range v.Services {
		ops := map[string][]registry.IAMAction{}
		names := map[string]bool{}
		for _, a := range s.Actions {
			if _, err := parseIAMAction(s.Prefix + ":" + a.Name); err != nil {
				return nil, failf(StageLoad, src.Source.Name, "%s actions: %w", s.Prefix, err)
			}
			names[a.Name] = true
		}
		for _, op := range s.Operations {
			acts := make([]registry.IAMAction, 0, len(op.Authorized))
			for _, a := range op.Authorized {
				act, err := parseIAMAction(a)
				if err != nil {
					return nil, failf(StageLoad, src.Source.Name, "%s %s: %w", s.Prefix, op.Name, err)
				}
				acts = append(acts, act)
			}
			ops[op.Name] = acts
		}
		t.byPrefix[s.Prefix] = iamService{operations: ops, actions: names}
	}
	return t, nil
}

// entry returns operation op's row of the table. Its authorized actions
// are in the table's order, which is alphabetical rather than primary
// first. A nil table gives every operation an empty row.
func (t *iamTable) entry(prefix, op string) (iamEntry, bool) {
	if t == nil {
		return iamEntry{}, true
	}
	s, ok := t.byPrefix[prefix]
	if !ok {
		return iamEntry{}, false
	}
	acts, ok := s.operations[op]
	return iamEntry{authorized: acts, listed: s.actions[op]}, ok
}

// primaryFirst orders operation op's actions primary first
// (smithy-models.md 8), the others following in the table's order.
//
// An action of op's own name that the service lists under Actions[] but
// op's authorized list leaves out is added as the primary: the Service
// Reference's mapping has gaps (RestoreTableToPointInTime authorizes only
// the item actions, StartLiveTail and AssumeRoleWithWebIdentity nothing),
// and smithy-models.md 8 prefers the same-name action for the used record.
// An operation with no action either way authorizes none.
//
// The primary action is the first of:
//   - the action of the same name under the service's own prefix;
//   - for a *Batch operation, its single-item form (SendMessageBatch is
//     sqs:SendMessage);
//   - the one action under the service's own prefix, which carries the
//     renames (HeadBucket is s3:ListBucket) and ranks it above iam:PassRole
//     and s3-object-lambda (PutBucketReplication is
//     s3:PutReplicationConfiguration);
//   - the iamPrimary entry for op in config, given in mapped.
//
// Any other operation fails, so an ambiguous entry the Service Reference
// adds cannot pick an alphabetical first silently; so does an entry the
// rules make redundant, or one naming an action op does not authorize.
func primaryFirst(config, prefix, op string, row iamEntry, mapped map[string]IAMPrimary) ([]registry.IAMAction, error) {
	entry, isMapped := mapped[op]
	acts := row.authorized
	if self := (registry.IAMAction{Prefix: prefix, Name: op}); row.listed && !slices.Contains(acts, self) {
		acts = append([]registry.IAMAction{self}, acts...)
	}
	if len(acts) == 0 {
		if isMapped {
			return nil, fmt.Errorf("the Service Reference lists no authorized action; remove its iamPrimary entry from %s", config)
		}
		return nil, nil
	}
	own := func(name string) func(registry.IAMAction) bool {
		return func(a registry.IAMAction) bool { return a.Prefix == prefix && a.Name == name }
	}
	primary := slices.IndexFunc(acts, own(op))
	if single, batch := strings.CutSuffix(op, "Batch"); primary < 0 && batch {
		primary = slices.IndexFunc(acts, own(single))
	}
	var owned []registry.IAMAction
	for _, a := range acts {
		if a.Prefix == prefix {
			owned = append(owned, a)
		}
	}
	if primary < 0 && len(owned) == 1 {
		primary = slices.Index(acts, owned[0])
	}
	switch {
	case primary >= 0 && isMapped:
		return nil, fmt.Errorf("the rules already make %s the primary action; remove its iamPrimary entry from %s", acts[primary], config)
	case isMapped:
		primary = slices.Index(acts, entry.Action)
		if primary < 0 {
			return nil, fmt.Errorf("the iamPrimary entry in %s names %s, which is not among the authorized actions %v", config, entry.Action, acts)
		}
	case primary < 0:
		return nil, fmt.Errorf("no rule picks the primary action among %v (%d under %s); add an iamPrimary entry {operation, action, reason} to %s", acts, len(owned), prefix, config)
	}
	out := append([]registry.IAMAction{acts[primary]}, acts[:primary]...)
	return append(out, acts[primary+1:]...), nil
}
