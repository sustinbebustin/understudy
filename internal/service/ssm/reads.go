package ssm

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// selector picks the version a GetParameter or GetParameters name reads
// (ssm-parameters.md 6.3): the latest, a version number or a label.
//
//sumtype:decl
type selector interface{ isSelector() }

// noSelector reads the latest version.
type noSelector struct{}

// versionSelector reads version n (name:<n>).
type versionSelector struct{ n int64 }

// labelSelector reads the version label names (name:<label>).
type labelSelector struct{ label string }

func (noSelector) isSelector()      {}
func (versionSelector) isSelector() {}
func (labelSelector) isSelector()   {}

// echo is the response's Selector for sel: the selector with its leading
// colon, absent for none (ssm-parameters.md 6.3; localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json:L5-L13).
func echo(sel selector) *string {
	switch sel := sel.(type) {
	case versionSelector:
		return new(":" + strconv.FormatInt(sel.n, 10))
	case labelSelector:
		return new(":" + sel.label)
	case noSelector:
	}
	return nil
}

// splitSelector cuts a :<version> or :<label> selector off a name or ARN
// (ssm-parameters.md 6.3): an all-digit selector is a version, any other a
// label. On an ARN the selector follows the resource. base keeps the
// request's other characters for parseNameOrARN, which judges them.
// UNVERIFIED (SSM-17), ssm-parameters.md 6.3: an empty selector (name:),
// one with a second colon (name:1:2) and a version too large to be one are
// understudy's ValidationException; AWS's answers are unrecorded.
func splitSelector(raw string) (string, selector, *awserr.Error) {
	trimmed := strings.Trim(raw, " ")
	prefix, rest := "", trimmed
	if strings.HasPrefix(trimmed, "arn:") {
		parts := strings.SplitN(trimmed, ":", 6)
		if len(parts) < 6 {
			// A malformed ARN carries no selector; parseNameOrARN refuses it.
			return trimmed, noSelector{}, nil
		}
		prefix, rest = strings.Join(parts[:5], ":")+":", parts[5]
	}
	base, text, found := strings.Cut(rest, ":")
	if !found {
		return trimmed, noSelector{}, nil
	}
	refuse := func(why string) (string, selector, *awserr.Error) {
		return "", nil, ssmapi.ErrValidationException(fmt.Sprintf(
			"Parameter %q has a selector that is not valid: %s. "+
				"Add one selector after the name: :<version number> or :<label>.", trimmed, why))
	}
	switch {
	case text == "":
		return refuse("nothing follows the colon")
	case strings.Contains(text, ":"):
		return refuse("it holds more than one colon")
	case strings.Trim(text, "0123456789") != "":
		return prefix + base, labelSelector{label: text}, nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return refuse(fmt.Sprintf("version %s is larger than any version number", text))
	}
	return prefix + base, versionSelector{n: n}, nil
}

// pick is the version of p that sel names.
func (p *Parameter) pick(sel selector) (Version, bool) {
	var number int64
	switch sel := sel.(type) {
	case noSelector:
		return p.latest(), true
	case versionSelector:
		number = sel.n
	case labelSelector:
		n, ok := p.Labels[sel.label]
		if !ok {
			return Version{}, false
		}
		number = n
	}
	i := slices.IndexFunc(p.Versions, func(v Version) bool { return v.Number == number })
	if i < 0 {
		return Version{}, false
	}
	return p.Versions[i], true
}

// errVersionNotFound is the answer for a selector that names no retained
// version of the parameter: never a fallback to the latest
// (ssm-parameters.md 6.1, 6.3). UNVERIFIED (SSM-17), ssm-parameters.md
// 6.3: the text is understudy's own, the version one after moto's.
func errVersionNotFound(spelling string, sel selector) *awserr.Error {
	what := "the version"
	switch sel := sel.(type) {
	case versionSelector:
		what = fmt.Sprintf("version %d", sel.n)
	case labelSelector:
		what = fmt.Sprintf("a version labeled %q", sel.label)
	case noSelector:
	}
	return ssmapi.ErrParameterVersionNotFound(fmt.Sprintf(
		"Systems Manager could not find %s of %s. Verify the selector and try again; GetParameterHistory lists the versions and their labels.",
		what, spelling))
}

// found is one name a read resolved: the parameter's canonical name, the
// spelling to echo, the selector and the version it picked.
type found struct {
	name name
	sel  selector
	v    Version
}

// find resolves a GetParameter or GetParameters name, with its selector,
// to a stored version, without touching KMS. A missing parameter is
// ParameterNotFound with the empty message AWS sends (localstack@8b9a79f
// tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114).
func (s *Service) find(scope aws.Scope, raw string) (found, *awserr.Error) {
	base, sel, err := splitSelector(raw)
	if err != nil {
		return found{}, err
	}
	n, err := parseNameOrARN(base, scope)
	if err != nil {
		return found{}, err
	}
	p, ok := s.lookup(scope, n.canonical)
	if !ok {
		return found{}, errNotFound()
	}
	v, ok := p.pick(sel)
	if !ok {
		return found{}, errVersionNotFound(n.spelling, sel)
	}
	return found{name: n, sel: sel, v: v}, nil
}

// render is f as a read returns it: Name echoes the request's spelling
// without the selector (switch ssm.name.root-slash-echo, ssm-parameters.md
// 3.2), Selector is present only when one was given, and the value is
// decrypted through KMS when decrypt asks (ssm-parameters.md 5.3).
func (s *Service) render(ctx context.Context, c *kernel.Call, scope aws.Scope, f found, decrypt bool) (ssmapi.Parameter, *awserr.Error) {
	text, err := s.valueText(ctx, c, scope, f.name.canonical, f.v.Value, decrypt)
	if err != nil {
		return ssmapi.Parameter{}, err
	}
	arn := parameterARN(scope, f.name.canonical)
	c.Touch(arn)
	return ssmapi.Parameter{
		Name: new(f.name.spelling), Type: new(ssmapi.ParameterType(f.v.Type)), Value: new(text), Version: f.v.Number,
		Selector: echo(f.sel), LastModifiedDate: new(f.v.LastModified.Wire()), ARN: new(arn.String()), DataType: new(f.v.DataType),
	}, nil
}

// getParameter reads one parameter by name or ARN, the latest version or
// the one a selector names (ssm-parameters.md 7.2).
func (s *Service) getParameter(ctx context.Context, c *kernel.Call, in *ssmapi.GetParameterInput) (*ssmapi.GetParameterOutput, *awserr.Error) {
	if err := checkReserved("GetParameter", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	f, err := s.find(scope, in.Name)
	if err != nil {
		return nil, err
	}
	p, err := s.render(ctx, c, scope, f, in.WithDecryption != nil && *in.WithDecryption)
	if err != nil {
		return nil, err
	}
	return &ssmapi.GetParameterOutput{Parameter: &p}, nil
}

// getParameters reads up to ten parameters (ssm-parameters.md 7.3): the
// ones found in byte order of their echoed names, the others, as the
// request gave them, in InvalidParameters, which is [] rather than absent
// when every name was found (localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json:L57-L74). A KMS failure
// on a decrypting read fails the whole call.
// UNVERIFIED (SSM-17, SSM-18), ssm-parameters.md 6.3, 7.3: a name given
// twice is answered once, and an invalid name or a selector naming no
// version is listed in InvalidParameters rather than failing the call.
func (s *Service) getParameters(ctx context.Context, c *kernel.Call, in *ssmapi.GetParametersInput) (*ssmapi.GetParametersOutput, *awserr.Error) {
	if err := checkReserved("GetParameters", in.Names...); err != nil {
		return nil, err
	}
	scope := c.Scope()
	decrypt := in.WithDecryption != nil && *in.WithDecryption
	out := &ssmapi.GetParametersOutput{Parameters: []ssmapi.Parameter{}, InvalidParameters: []string{}}
	seen := make(map[string]bool, len(in.Names))
	for _, raw := range in.Names {
		asked := strings.Trim(raw, " ")
		if seen[asked] {
			continue
		}
		seen[asked] = true
		f, err := s.find(scope, asked)
		if err != nil {
			out.InvalidParameters = append(out.InvalidParameters, asked)
			continue
		}
		p, err := s.render(ctx, c, scope, f, decrypt)
		if err != nil {
			return nil, err
		}
		out.Parameters = append(out.Parameters, p)
	}
	slices.SortStableFunc(out.Parameters, func(a, b ssmapi.Parameter) int { return cmp.Compare(*a.Name, *b.Name) })
	return out, nil
}
