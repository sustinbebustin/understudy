package ssm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
)

// historyPageSize is GetParameterHistory's page without MaxResults
// (ssm-parameters.md 6.1; SSM-30 step 03, 50 of 100 versions and a
// NextToken).
const historyPageSize = 50

// listingHistory is GetParameterHistory's listing, named inside the
// NextTokens it issues.
const listingHistory paging.Listing = "ssm.parameter-history"

// maxLabelsPerVersion is how many labels one version can carry
// (ssm-parameters.md 6.2).
const maxLabelsPerVersion = 10

// versionKey orders versions by number as text: zero-padded, so byte order
// is numeric order.
func versionKey(v Version) string { return fmt.Sprintf("%020d", v.Number) }

// labelsOf is the labels naming version n of p, sorted, [] when none
// (ssm-parameters.md 6.2; SSM-16 step 04 records the empty list).
// UNVERIFIED (SSM-16): the order of several labels.
func (p *Parameter) labelsOf(n int64) []string {
	out := []string{}
	for l, v := range p.Labels {
		if v == n {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

// getParameterHistory lists a parameter's retained versions oldest first
// (ssm-parameters.md 7.6), each with its labels and Policies [] (7.10). Name
// is the canonical name (switch ssm.name.root-slash-echo) and KeyId is
// reported for a SecureString only (switch ssm.kms.key-id-echo). Values are
// decrypted when asked, after the state is read, for the page's versions
// only; a KMS failure fails the call. A missing parameter is
// ParameterNotFound with no message (switch
// ssm.error.parameter-not-found-message).
func (s *Service) getParameterHistory(ctx context.Context, c *kernel.Call, in *ssmapi.GetParameterHistoryInput) (*ssmapi.GetParameterHistoryOutput, *awserr.Error) {
	if err := checkReserved("GetParameterHistory", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseNameOrARN(in.Name, askerOf(c))
	if err != nil {
		return nil, err
	}
	p, ok := s.lookup(scope, n.canonical)
	if !ok {
		return nil, errNotFound()
	}
	page, err := pageOf(listingHistory, slices.Clone(p.Versions), versionKey, in.MaxResults, historyPageSize, in.NextToken)
	if err != nil {
		return nil, err
	}
	decrypt := in.WithDecryption != nil && *in.WithDecryption
	out := &ssmapi.GetParameterHistoryOutput{Parameters: make([]ssmapi.ParameterHistory, 0, len(page.Items)), NextToken: page.Next}
	for i := range page.Items {
		v := &page.Items[i]
		text, err := s.valueText(ctx, c, scope, p.Name, v.Value, decrypt)
		if err != nil {
			return nil, err
		}
		out.Parameters = append(out.Parameters, ssmapi.ParameterHistory{
			Name: new(p.Name), Type: new(ssmapi.ParameterType(v.Type)), KeyId: keyIDOf(v.Value),
			LastModifiedDate: new(v.LastModified.Wire()), LastModifiedUser: new(v.LastModifiedUser),
			Description: v.Description, Value: new(text), AllowedPattern: v.AllowedPattern, Version: v.Number,
			Labels: p.labelsOf(v.Number), Tier: new(ssmapi.ParameterTier(v.Tier)),
			Policies: []ssmapi.ParameterInlinePolicy{}, DataType: new(v.DataType),
		})
	}
	c.Touch(parameterARN(scope, p.Name))
	return out, nil
}

// validLabel reports whether label meets the labeling rules
// (ssm-parameters.md 6.2 [D]): letters, digits, periods, hyphens and
// underscores only, not beginning with a digit, aws or ssm (any case). The
// generated @length(1, 100) has already refused the empty and the long.
// Labels holding : or / are refused by the character rule, as AWS lists
// them in InvalidLabels (ssm-parameters.md 6.2; SSM-16 step 01).
func validLabel(label string) bool {
	lower := strings.ToLower(label)
	switch {
	case label == "", label[0] >= '0' && label[0] <= '9':
		return false
	case strings.HasPrefix(lower, "aws"), strings.HasPrefix(lower, "ssm"):
		return false
	}
	return strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == ""
}

// distinct is labels without repeats, in first-given order.
func distinct(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// errLabelLimit is LabelParameterVersion's answer when a version would
// carry more than ten labels, AWS's text with no space after its first
// period (ssm-parameters.md 6.2; SSM-16 step 06).
func errLabelLimit() *awserr.Error {
	return ssmapi.ErrParameterVersionLabelLimitExceeded("A parameter version can have maximum 10 labels.Move one or more labels to another version and try again.")
}

// errLabelTargetNotFound is LabelParameterVersion's and
// UnlabelParameterVersion's answer for a missing parameter, which names it
// as the request spelled it (switch ssm.error.parameter-not-found-message;
// ssm-parameters.md 2.2, SSM-02 steps 03-04).
func errLabelTargetNotFound(spelling string) *awserr.Error {
	return ssmapi.ErrParameterNotFound("Parameter " + spelling + " not found.")
}

// labelOutcome is what a label write did: the version it labeled and the
// labels it refused.
type labelOutcome struct {
	version int64
	invalid []string
}

// labelParameterVersion attaches labels to a version, the latest when none
// is given (ssm-parameters.md 6.2, 7.8). A label breaking the labeling rules
// is listed in InvalidLabels and not applied, with a 200; a valid label on
// another version moves. When the version would then carry more than ten
// labels nothing is applied. Labeling changes no LastModifiedDate
// (ssm-parameters.md 6.1; SSM-08 steps 02-03).
func (s *Service) labelParameterVersion(_ context.Context, c *kernel.Call, in *ssmapi.LabelParameterVersionInput) (*ssmapi.LabelParameterVersionOutput, *awserr.Error) {
	if err := checkReserved("LabelParameterVersion", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, askerOf(c))
	if err != nil {
		return nil, err
	}
	o, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (labelOutcome, *awserr.Error) {
		prev, ok := st.Parameters[n.canonical]
		if !ok {
			tx.Unchanged()
			return labelOutcome{}, errLabelTargetNotFound(n.spelling)
		}
		p, out, err := withLabels(prev, n.spelling, in.ParameterVersion, in.Labels)
		if err != nil || maps.Equal(p.Labels, prev.Labels) {
			tx.Unchanged()
			return out, err
		}
		st.Parameters[n.canonical] = p
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.LabelParameterVersionOutput{InvalidLabels: o.invalid, ParameterVersion: o.version}, nil
}

// withLabels is p with labels attached to version (the latest when nil),
// and what that did. The caller's parameter is not modified.
func withLabels(p Parameter, spelling string, version *int64, labels []string) (Parameter, labelOutcome, *awserr.Error) {
	var sel selector = noSelector{}
	if version != nil {
		sel = versionSelector{n: *version}
	}
	v, ok := p.pick(sel)
	if !ok {
		return Parameter{}, labelOutcome{}, errVersionNotFound(spelling, sel)
	}
	out := labelOutcome{version: v.Number, invalid: []string{}}
	p.Labels = maps.Clone(p.Labels)
	for _, l := range distinct(labels) {
		if !validLabel(l) {
			out.invalid = append(out.invalid, l)
			continue
		}
		p.Labels[l] = v.Number
	}
	if count := len(p.labelsOf(v.Number)); count > maxLabelsPerVersion {
		return Parameter{}, labelOutcome{}, errLabelLimit()
	}
	return p, out, nil
}

// unlabelOutcome is what an unlabel write did.
type unlabelOutcome struct{ removed, invalid []string }

// unlabelParameterVersion removes labels from a version
// (ssm-parameters.md 6.2, 7.8): the labels attached to it are removed and
// listed in RemovedLabels, the others in InvalidLabels, both in request
// order and [] when empty.
func (s *Service) unlabelParameterVersion(_ context.Context, c *kernel.Call, in *ssmapi.UnlabelParameterVersionInput) (*ssmapi.UnlabelParameterVersionOutput, *awserr.Error) {
	if err := checkReserved("UnlabelParameterVersion", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, askerOf(c))
	if err != nil {
		return nil, err
	}
	o, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (unlabelOutcome, *awserr.Error) {
		p, ok := st.Parameters[n.canonical]
		if !ok {
			tx.Unchanged()
			return unlabelOutcome{}, errLabelTargetNotFound(n.spelling)
		}
		sel := versionSelector{n: in.ParameterVersion}
		v, ok := p.pick(sel)
		if !ok {
			tx.Unchanged()
			return unlabelOutcome{}, errVersionNotFound(n.spelling, sel)
		}
		out := unlabelOutcome{removed: []string{}, invalid: []string{}}
		labels := maps.Clone(p.Labels)
		for _, l := range distinct(in.Labels) {
			if on, ok := labels[l]; ok && on == v.Number {
				delete(labels, l)
				out.removed = append(out.removed, l)
				continue
			}
			out.invalid = append(out.invalid, l)
		}
		if len(out.removed) == 0 {
			tx.Unchanged()
			return out, nil
		}
		p.Labels = labels
		st.Parameters[n.canonical] = p
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.UnlabelParameterVersionOutput{RemovedLabels: o.removed, InvalidLabels: o.invalid}, nil
}
