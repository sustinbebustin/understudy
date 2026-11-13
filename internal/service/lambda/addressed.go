package lambda

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// declareAddressed names, for every operation whose input addresses a
// function, version, alias, event source mapping, layer or layer version,
// the ARN it addresses,
// built from the name as Invoke's is (invokedFunction): the function
// qualified as the input names it, an alias's ARN, a mapping's ARN, a tag
// operation's Resource. Step 8 then asks about the operation's actions on
// that resource, which AWS evaluates the call against (iam-evaluation.md
// 2.1) and whose ARN the name gives before any lookup, so a missing
// function is asked about before ResourceNotFoundException answers it
// (iam-enforcement-surfaces.md 5.3, switch iam.enforce.missing-resource).
// Without it step 8 asked about "*", which a policy granting the action on
// the function alone does not allow, while the access record kept the
// function the handler touched (12.6). A name the resolver cannot read
// names nothing, and the handler answers it. It runs after the bind sites,
// whose bindings it extends.
func declareAddressed(b *kernel.Binder) {
	byName(b, lambdaapi.OpCreateFunction, func(in *lambdaapi.CreateFunctionInput) (string, *string) { return in.FunctionName, nil })
	byName(b, lambdaapi.OpGetFunction, func(in *lambdaapi.GetFunctionInput) (string, *string) { return in.FunctionName, in.Qualifier })
	byName(b, lambdaapi.OpGetFunctionConfiguration, func(in *lambdaapi.GetFunctionConfigurationInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpUpdateFunctionConfiguration, func(in *lambdaapi.UpdateFunctionConfigurationInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpUpdateFunctionCode, func(in *lambdaapi.UpdateFunctionCodeInput) (string, *string) { return in.FunctionName, nil })
	byName(b, lambdaapi.OpDeleteFunction, func(in *lambdaapi.DeleteFunctionInput) (string, *string) { return in.FunctionName, in.Qualifier })
	byName(b, lambdaapi.OpGetFunctionCodeSigningConfig, func(in *lambdaapi.GetFunctionCodeSigningConfigInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpPublishVersion, func(in *lambdaapi.PublishVersionInput) (string, *string) { return in.FunctionName, nil })
	byName(b, lambdaapi.OpListVersionsByFunction, func(in *lambdaapi.ListVersionsByFunctionInput) (string, *string) {
		return in.FunctionName, nil
	})
	byAlias(b, lambdaapi.OpCreateAlias, func(in *lambdaapi.CreateAliasInput) (string, string) { return in.FunctionName, in.Name })
	byAlias(b, lambdaapi.OpGetAlias, func(in *lambdaapi.GetAliasInput) (string, string) { return in.FunctionName, in.Name })
	byAlias(b, lambdaapi.OpUpdateAlias, func(in *lambdaapi.UpdateAliasInput) (string, string) { return in.FunctionName, in.Name })
	byAlias(b, lambdaapi.OpDeleteAlias, func(in *lambdaapi.DeleteAliasInput) (string, string) { return in.FunctionName, in.Name })
	byName(b, lambdaapi.OpListAliases, func(in *lambdaapi.ListAliasesInput) (string, *string) { return in.FunctionName, nil })
	byName(b, lambdaapi.OpAddPermission, func(in *lambdaapi.AddPermissionInput) (string, *string) { return in.FunctionName, in.Qualifier })
	byName(b, lambdaapi.OpGetPolicy, func(in *lambdaapi.GetPolicyInput) (string, *string) { return in.FunctionName, in.Qualifier })
	byName(b, lambdaapi.OpRemovePermission, func(in *lambdaapi.RemovePermissionInput) (string, *string) { return in.FunctionName, in.Qualifier })
	byName(b, lambdaapi.OpPutFunctionConcurrency, func(in *lambdaapi.PutFunctionConcurrencyInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpGetFunctionConcurrency, func(in *lambdaapi.GetFunctionConcurrencyInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpDeleteFunctionConcurrency, func(in *lambdaapi.DeleteFunctionConcurrencyInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpPutFunctionEventInvokeConfig, func(in *lambdaapi.PutFunctionEventInvokeConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpUpdateFunctionEventInvokeConfig, func(in *lambdaapi.UpdateFunctionEventInvokeConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpGetFunctionEventInvokeConfig, func(in *lambdaapi.GetFunctionEventInvokeConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpDeleteFunctionEventInvokeConfig, func(in *lambdaapi.DeleteFunctionEventInvokeConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpListFunctionEventInvokeConfigs, func(in *lambdaapi.ListFunctionEventInvokeConfigsInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpCreateFunctionUrlConfig, func(in *lambdaapi.CreateFunctionUrlConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpGetFunctionUrlConfig, func(in *lambdaapi.GetFunctionUrlConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpUpdateFunctionUrlConfig, func(in *lambdaapi.UpdateFunctionUrlConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpDeleteFunctionUrlConfig, func(in *lambdaapi.DeleteFunctionUrlConfigInput) (string, *string) {
		return in.FunctionName, in.Qualifier
	})
	byName(b, lambdaapi.OpListFunctionUrlConfigs, func(in *lambdaapi.ListFunctionUrlConfigsInput) (string, *string) {
		return in.FunctionName, nil
	})
	byName(b, lambdaapi.OpCreateEventSourceMapping, func(in *lambdaapi.CreateEventSourceMappingInput) (string, *string) {
		return in.FunctionName, nil
	})
	byTagTarget(b, lambdaapi.OpTagResource, func(in *lambdaapi.TagResourceInput) string { return in.Resource })
	byTagTarget(b, lambdaapi.OpUntagResource, func(in *lambdaapi.UntagResourceInput) string { return in.Resource })
	byTagTarget(b, lambdaapi.OpListTags, func(in *lambdaapi.ListTagsInput) string { return in.Resource })
	byMapping(b, lambdaapi.OpGetEventSourceMapping, func(in *lambdaapi.GetEventSourceMappingInput) string { return in.UUID })
	byMapping(b, lambdaapi.OpUpdateEventSourceMapping, func(in *lambdaapi.UpdateEventSourceMappingInput) string { return in.UUID })
	byMapping(b, lambdaapi.OpDeleteEventSourceMapping, func(in *lambdaapi.DeleteEventSourceMappingInput) string { return in.UUID })
	byLayer(b, lambdaapi.OpPublishLayerVersion, func(in *lambdaapi.PublishLayerVersionInput) string { return in.LayerName })
	byLayer(b, lambdaapi.OpListLayerVersions, func(in *lambdaapi.ListLayerVersionsInput) string { return in.LayerName })
	byLayerVersion(b, lambdaapi.OpGetLayerVersion, func(in *lambdaapi.GetLayerVersionInput) (string, int64) {
		return in.LayerName, in.VersionNumber
	})
	byLayerVersion(b, lambdaapi.OpDeleteLayerVersion, func(in *lambdaapi.DeleteLayerVersionInput) (string, int64) {
		return in.LayerName, in.VersionNumber
	})
	byLayerVersion(b, lambdaapi.OpAddLayerVersionPermission, func(in *lambdaapi.AddLayerVersionPermissionInput) (string, int64) {
		return in.LayerName, in.VersionNumber
	})
	byLayerVersion(b, lambdaapi.OpRemoveLayerVersionPermission, func(in *lambdaapi.RemoveLayerVersionPermissionInput) (string, int64) {
		return in.LayerName, in.VersionNumber
	})
	byLayerVersion(b, lambdaapi.OpGetLayerVersionPolicy, func(in *lambdaapi.GetLayerVersionPolicyInput) (string, int64) {
		return in.LayerName, in.VersionNumber
	})
	kernel.Declare(b, lambdaapi.OpGetLayerVersionByArn, kernel.WithResources(func(_ *kernel.Call, in *lambdaapi.GetLayerVersionByArnInput) []aws.ARN {
		ref, n, ok := parseLayerVersionARN(in.Arn)
		if !ok {
			return nil
		}
		return parsedARN(ref.versionARN(n))
	}))
}

// byLayer declares the layer op's LayerName address: the layer's ARN.
func byLayer[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], name func(*In) string) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		ref, err := parseLayerName(c, name(in))
		if err != nil {
			return nil
		}
		return parsedARN(ref.arn())
	}))
}

// byLayerVersion declares the layer op's LayerName and VersionNumber
// address: the layer version's ARN.
func byLayerVersion[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], version func(*In) (string, int64)) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		name, n := version(in)
		ref, err := parseLayerName(c, name)
		if err != nil {
			return nil
		}
		return parsedARN(ref.versionARN(n))
	}))
}

// byName declares the function op's FunctionName and Qualifier address,
// qualified as they name it.
func byName[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], name func(*In) (string, *string)) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		fn, qualifier := name(in)
		r, err := parseFunctionRef(c, fn, qualifier)
		if err != nil {
			return nil
		}
		return parsedARN(r.qualifiedARN())
	}))
}

// byAlias declares the alias op's FunctionName and alias Name address.
func byAlias[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], alias func(*In) (string, string)) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		fn, name := alias(in)
		r, err := parseFunctionRef(c, fn, nil)
		if err != nil {
			return nil
		}
		return parsedARN(r.aliasARN(name))
	}))
}

// byTagTarget declares the function or mapping a tag operation's Resource
// names (parseTaggable).
func byTagTarget[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], resource func(*In) string) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		t, err := parseTaggable(c, resource(in))
		if err != nil {
			return nil
		}
		return parsedARN(t.arn())
	}))
}

// byMapping declares the event source mapping op's UUID addresses, in the
// caller's scope; a UUID holding ':' or '/' names nothing.
func byMapping[In, Out any](b *kernel.Binder, op kernel.Op[In, Out], uuid func(*In) string) {
	kernel.Declare(b, op, kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		id := uuid(in)
		if id == "" || strings.ContainsAny(id, ":/") {
			return nil
		}
		return parsedARN(mappingARN(c.Scope(), id))
	}))
}

// parsedARN is the ARN s spells, or nothing when it does not parse.
func parsedARN(s string) []aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		return nil
	}
	return []aws.ARN{a}
}
