package lambda

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// maxURLConfigsPage caps a ListFunctionUrlConfigs page (lambda-api.md 7.6).
const maxURLConfigsPage = 50

func (s *Service) bindURLConfigs(b lambdaapi.Binder) {
	const note = "the URL is served (see InvokeFunctionUrl); InvokeMode RESPONSE_STREAM answers buffered and CORS is applied as stored"
	b.CreateFunctionUrlConfig(s.createFunctionURLConfig, partial(note))
	b.GetFunctionUrlConfig(s.getFunctionURLConfig, full())
	b.UpdateFunctionUrlConfig(s.updateFunctionURLConfig, partial(note))
	b.DeleteFunctionUrlConfig(s.deleteFunctionURLConfig, full())
	b.ListFunctionUrlConfigs(s.listFunctionURLConfigs, full())
}

// urlTarget finds the function and alias a URL configuration belongs to;
// false when either is missing. Create, Update and List answer that with
// "Function does not exist" (lambda-events.md 7.1), Get and Delete with
// "The resource you requested does not exist." (test_url_config_exceptions).
func (st *State) urlTarget(ref functionRef) (Function, bool) {
	f, ok := st.Functions[ref.name]
	if !ok {
		return Function{}, false
	}
	if _, ok := f.Aliases[ref.qualifier]; ref.qualifier != "" && !ok {
		return Function{}, false
	}
	return f, true
}

// urlConfig finds the URL configuration ref names; false when it, its
// function or its alias is missing.
func (st *State) urlConfig(ref functionRef) (URLConfig, bool) {
	f, ok := st.urlTarget(ref)
	if !ok {
		return URLConfig{}, false
	}
	u, ok := f.URLs[ref.qualifier]
	return u, ok
}

func (s *Service) createFunctionURLConfig(_ context.Context, c *kernel.Call, in *lambdaapi.CreateFunctionUrlConfigInput) (*lambdaapi.CreateFunctionUrlConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	u, err := write(s, c, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (URLConfig, *awserr.Error) {
		f, ok := st.urlTarget(ref)
		if !ok {
			return URLConfig{}, errURLFunctionMissing()
		}
		if _, ok := f.URLs[ref.qualifier]; ok {
			return URLConfig{}, errURLConfigExists(ref.qualifiedARN())
		}
		u := URLConfig{ID: st.urlID(ref.scope, c.ID()), AuthType: string(in.AuthType), InvokeMode: string(deref(in.InvokeMode)), Cors: fromCors(in.Cors), Created: now, Modified: now}
		f = f.clone()
		if f.URLs == nil {
			f.URLs = map[string]URLConfig{}
		}
		f.URLs[ref.qualifier] = u
		st.Functions[ref.name] = f
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &lambdaapi.CreateFunctionUrlConfigOutput{
		FunctionUrl:  functionURL(c.Endpoint(), ref.scope, u.ID),
		FunctionArn:  ref.qualifiedARN(),
		AuthType:     lambdaapi.FunctionUrlAuthType(u.AuthType),
		Cors:         toCors(u.Cors),
		CreationTime: urlTime(u.Created),
		InvokeMode:   echoedInvokeMode(u),
	}, nil
}

// echoedInvokeMode is InvokeMode as Create and Update answer it: present
// only once a request has set it, as AWS-recorded responses leave it out
// otherwise (LocalStack's AWS-validated test_url_config_lifecycle).
func echoedInvokeMode(u URLConfig) *lambdaapi.InvokeMode {
	if u.InvokeMode == "" {
		return nil
	}
	return new(lambdaapi.InvokeMode(u.InvokeMode))
}

// renderURLConfig is a configuration as the read operations report it,
// with InvokeMode always present (Terraform's invoke_mode is not computed).
func renderURLConfig(c *kernel.Call, ref functionRef, u URLConfig) lambdaapi.FunctionUrlConfig {
	mode := lambdaapi.InvokeModeBuffered
	if u.InvokeMode != "" {
		mode = lambdaapi.InvokeMode(u.InvokeMode)
	}
	return lambdaapi.FunctionUrlConfig{
		FunctionUrl:      functionURL(c.Endpoint(), ref.scope, u.ID),
		FunctionArn:      ref.qualifiedARN(),
		AuthType:         lambdaapi.FunctionUrlAuthType(u.AuthType),
		Cors:             toCors(u.Cors),
		CreationTime:     urlTime(u.Created),
		LastModifiedTime: urlTime(u.Modified),
		InvokeMode:       new(mode),
	}
}

func (s *Service) getFunctionURLConfig(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionUrlConfigInput) (*lambdaapi.GetFunctionUrlConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	u, err := view(s, ref.scope, func(st *State) (URLConfig, *awserr.Error) {
		u, ok := st.urlConfig(ref)
		if !ok {
			return URLConfig{}, errResourceNotFound()
		}
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	r := renderURLConfig(c, ref, u)
	return &lambdaapi.GetFunctionUrlConfigOutput{
		FunctionUrl: r.FunctionUrl, FunctionArn: r.FunctionArn, AuthType: r.AuthType, Cors: r.Cors,
		CreationTime: r.CreationTime, LastModifiedTime: r.LastModifiedTime, InvokeMode: r.InvokeMode,
	}, nil
}

func (s *Service) updateFunctionURLConfig(_ context.Context, c *kernel.Call, in *lambdaapi.UpdateFunctionUrlConfigInput) (*lambdaapi.UpdateFunctionUrlConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	u, err := write(s, c, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (URLConfig, *awserr.Error) {
		f, ok := st.urlTarget(ref)
		if !ok {
			return URLConfig{}, errURLFunctionMissing()
		}
		u, ok := f.URLs[ref.qualifier]
		if !ok {
			return URLConfig{}, errResourceNotFound()
		}
		if in.AuthType != nil {
			u.AuthType = string(*in.AuthType)
		}
		if in.InvokeMode != nil {
			u.InvokeMode = string(*in.InvokeMode)
		}
		if in.Cors != nil {
			u.Cors = fromCors(in.Cors)
		}
		u.Modified = now
		f = f.clone()
		f.URLs[ref.qualifier] = u
		st.Functions[ref.name] = f
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	r := renderURLConfig(c, ref, u)
	return &lambdaapi.UpdateFunctionUrlConfigOutput{
		FunctionUrl: r.FunctionUrl, FunctionArn: r.FunctionArn, AuthType: r.AuthType, Cors: r.Cors,
		CreationTime: r.CreationTime, LastModifiedTime: r.LastModifiedTime, InvokeMode: echoedInvokeMode(u),
	}, nil
}

func (s *Service) deleteFunctionURLConfig(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteFunctionUrlConfigInput) (*lambdaapi.DeleteFunctionUrlConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		u, ok := st.urlConfig(ref)
		if !ok {
			return struct{}{}, errResourceNotFound()
		}
		f := st.Functions[ref.name].clone()
		delete(f.URLs, ref.qualifier)
		st.retireURLs(u)
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &lambdaapi.DeleteFunctionUrlConfigOutput{}, nil
}

func (s *Service) listFunctionURLConfigs(_ context.Context, c *kernel.Call, in *lambdaapi.ListFunctionUrlConfigsInput) (*lambdaapi.ListFunctionUrlConfigsOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	type row struct {
		qualifier string
		config    URLConfig
	}
	rows, err := view(s, ref.scope, func(st *State) ([]row, *awserr.Error) {
		f, ok := st.urlTarget(functionRef{scope: ref.scope, name: ref.name})
		if !ok {
			return nil, errURLFunctionMissing()
		}
		var rows []row
		for _, q := range slices.Sorted(maps.Keys(f.URLs)) {
			rows = append(rows, row{qualifier: q, config: f.URLs[q]})
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	p, err := page(rows, func(r row) string { return r.qualifier }, in.MaxItems, maxURLConfigsPage, in.Marker)
	if err != nil {
		return nil, err
	}
	out := &lambdaapi.ListFunctionUrlConfigsOutput{FunctionUrlConfigs: []lambdaapi.FunctionUrlConfig{}, NextMarker: p.next}
	for _, r := range p.items {
		out.FunctionUrlConfigs = append(out.FunctionUrlConfigs, renderURLConfig(c, functionRef{scope: ref.scope, name: ref.name, qualifier: r.qualifier}, r.config))
	}
	return out, nil
}

// urlTime renders a function URL timestamp as AWS does: ISO 8601 with
// microseconds, in UTC.
func urlTime(at clock.Instant) string {
	return at.Wire().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// fromCors stores a CORS configuration; an empty one clears it, as
// Terraform sends Cors {} to remove the block.
func fromCors(c *lambdaapi.Cors) *Cors {
	if c == nil || (c.AllowCredentials == nil && c.MaxAge == nil && len(c.AllowHeaders)+len(c.AllowMethods)+len(c.AllowOrigins)+len(c.ExposeHeaders) == 0) {
		return nil
	}
	return &Cors{
		AllowCredentials: c.AllowCredentials, MaxAge: c.MaxAge,
		AllowHeaders: slices.Clone(c.AllowHeaders), AllowMethods: slices.Clone(c.AllowMethods),
		AllowOrigins: slices.Clone(c.AllowOrigins), ExposeHeaders: slices.Clone(c.ExposeHeaders),
	}
}

// toCors reports a stored CORS configuration; nil when none is set, which
// Terraform reads as no cors block.
func toCors(c *Cors) *lambdaapi.Cors {
	if c == nil {
		return nil
	}
	return &lambdaapi.Cors{
		AllowCredentials: c.AllowCredentials, MaxAge: c.MaxAge,
		AllowHeaders: slices.Clone(c.AllowHeaders), AllowMethods: slices.Clone(c.AllowMethods),
		AllowOrigins: slices.Clone(c.AllowOrigins), ExposeHeaders: slices.Clone(c.ExposeHeaders),
	}
}
