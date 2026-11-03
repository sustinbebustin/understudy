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
// request's other characters for parseNameOrARN, which judges them. A
// second colon (name:1:2) answers AWS's ValidationException
// (ssm-parameters.md 6.3; SSM-17 step 06, two spaces before <name> in
// AWS's text). UNVERIFIED (SSM-17), ssm-parameters.md 6.3: an empty
// selector (name:) and a version too large to be one answer the same.
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
	refused := ssmapi.ErrValidationException("Invalid parameter name. Please use correct syntax for referencing a version/label  <name>:<version/label>")
	switch {
	case text == "", strings.Contains(text, ":"):
		return "", nil, refused
	case strings.Trim(text, "0123456789") != "":
		return prefix + base, labelSelector{label: text}, nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return "", nil, refused
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
// version of the parameter, never a fallback to the latest, in AWS's text
// naming the parameter as the request spelled it (ssm-parameters.md 6.1,
// 6.3; SSM-17 steps 03-05, SSM-30 step 04). A missing latest version
// cannot happen; its text is understudy's own.
func errVersionNotFound(spelling string, sel selector) *awserr.Error {
	switch sel := sel.(type) {
	case versionSelector:
		return ssmapi.ErrParameterVersionNotFound(fmt.Sprintf(
			"Systems Manager could not find version %d of %s. Verify the version and try again.", sel.n, spelling))
	case labelSelector:
		return ssmapi.ErrParameterVersionNotFound(fmt.Sprintf(
			"Systems Manager could not find a version of %s that has the specified label, %s. Verify the parameter label and try again.", spelling, sel.label))
	case noSelector:
	}
	return ssmapi.ErrParameterVersionNotFound("Systems Manager could not find the latest version of " + spelling + ".")
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
// ParameterNotFound with no message (ssm-parameters.md 2.2; SSM-01 step
// 01).
func (s *Service) find(a asker, raw string) (found, *awserr.Error) {
	base, sel, err := splitSelector(raw)
	if err != nil {
		return found{}, err
	}
	n, err := parseNameOrARN(base, a)
	if err != nil {
		return found{}, err
	}
	p, ok := s.lookup(a.scope, n.canonical)
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
	f, err := s.find(askerOf(c), in.Name)
	if err != nil {
		return nil, err
	}
	p, err := s.render(ctx, c, c.Scope(), f, in.WithDecryption != nil && *in.WithDecryption)
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
// on a decrypting read fails the whole call. A name given twice is answered
// once, and each spelling of one parameter once with its own Name (SSM-18
// step 01; SSM-07 step 03, /foo and foo); an invalid name or a selector
// naming no version is listed in InvalidParameters, sorted, rather than
// failing the call (SSM-17 step 08, SSM-18 step 02). UNVERIFIED (SSM-07),
// ssm-parameters.md 3.2: a parameter asked by name and by ARN is answered
// twice too.
func (s *Service) getParameters(ctx context.Context, c *kernel.Call, in *ssmapi.GetParametersInput) (*ssmapi.GetParametersOutput, *awserr.Error) {
	if err := checkReserved("GetParameters", in.Names...); err != nil {
		return nil, err
	}
	scope, a := c.Scope(), askerOf(c)
	decrypt := in.WithDecryption != nil && *in.WithDecryption
	out := &ssmapi.GetParametersOutput{Parameters: []ssmapi.Parameter{}, InvalidParameters: []string{}}
	seen := make(map[string]bool, len(in.Names))
	for _, raw := range in.Names {
		asked := strings.Trim(raw, " ")
		if seen[asked] {
			continue
		}
		seen[asked] = true
		f, err := s.find(a, asked)
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
	slices.Sort(out.InvalidParameters)
	return out, nil
}
