package ssm

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// What step 8 asks about each Parameter Store call (spec "Contract" IAM;
// iam-enforcement-surfaces.md 12.3): the operation's ssm: action on the
// parameter ARNs the input names (ssm-parameters.md 3.3). A name that does
// not parse names nothing, and the handler refuses it as usual.
// DescribeParameters names no resource.

// byName names the one parameter (or parameter ARN, with an optional
// selector) an input addresses.
func byName[In any](nameOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return resourcesOf(askerOf(c), nameOf(in))
	})
}

// byNames names one parameter ARN per name of GetParameters and
// DeleteParameters.
func byNames[In any](namesOf func(*In) []string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return resourcesOf(askerOf(c), namesOf(in)...)
	})
}

// resourcesOf is the parameter ARN of each raw name, ARN or selected name.
// An ARN of another account keeps its account, so step 8 asks about the
// parameter the caller named (spec enforce matrix); one of another Region
// or resource type names nothing, the handler refusing it as malformed
// (SSM-29).
func resourcesOf(a asker, raws ...string) []aws.ARN {
	out := make([]aws.ARN, 0, len(raws))
	for _, raw := range raws {
		base, _, err := splitSelector(raw)
		if err != nil {
			continue
		}
		n, aerr := parseNameOrARN(base, a)
		switch {
		case aerr == nil:
			out = append(out, parameterARN(a.scope, n.canonical))
		case aerr.ID == ssmapi.IdentityAccessDeniedException():
			// A parameter ARN the caller may not reach: another account's,
			// or one past the length limit.
			if arn, err := aws.ParseARN(strings.Trim(base, " ")); err == nil {
				out = append(out, arn)
			}
		}
	}
	return out
}

// byPath names the path ARN GetParametersByPath is authorized on
// (ssm-parameters.md 7.4): parameter/ and the path as given without its
// leading slash, a trailing slash kept, so Path / is parameter/ and /a/b/
// is parameter/a/b/ (SSM-35 steps 02-03).
func byPath() kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *ssmapi.GetParametersByPathInput) []aws.ARN {
		if _, err := parsePath(in.Path, askerOf(c)); err != nil {
			return nil
		}
		return []aws.ARN{parameterARN(c.Scope(), strings.Trim(in.Path, " "))}
	})
}

// byTagTarget names the parameter a tag operation addresses, by name or
// ARN; a resource type other than Parameter names nothing understudy
// stores.
func byTagTarget[In any](target func(*In) (ssmapi.ResourceTypeForTagging, string)) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		rt, id := target(in)
		if rt != ssmapi.ResourceTypeForTaggingParameter {
			return nil
		}
		return resourcesOf(askerOf(c), id)
	})
}

// putActions declares ssm:AddTagsToResource beside ssm:PutParameter only
// for a put that carries Tags (spec "Contract" IAM; enforce matrix).
func putActions() kernel.BindOption {
	return kernel.WithActions(func(_ *kernel.Call, in *ssmapi.PutParameterInput) []registry.IAMAction {
		if len(in.Tags) == 0 {
			return nil
		}
		return []registry.IAMAction{{Prefix: "ssm", Name: "AddTagsToResource"}}
	})
}
