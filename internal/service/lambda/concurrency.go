package lambda

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

func (s *Service) bindConcurrency(b lambdaapi.Binder) {
	// Invoke enforces reserved concurrency, and what reservations leave of
	// the account's, before any container work (Execution).
	b.PutFunctionConcurrency(s.putFunctionConcurrency, full())
	b.GetFunctionConcurrency(s.getFunctionConcurrency, full())
	b.DeleteFunctionConcurrency(s.deleteFunctionConcurrency, full())
}

// functionOnly reads a FunctionName that must name the function itself,
// as reserved concurrency is function-level (lambda-api.md 7.4).
func functionOnly(c *kernel.Call, name string) (functionRef, *awserr.Error) {
	ref, err := parseFunctionRef(c, name, nil)
	if err != nil {
		return functionRef{}, err
	}
	if ref.qualifier != "" {
		return functionRef{}, errFunctionsOnly()
	}
	return ref, nil
}

func (s *Service) putFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.PutFunctionConcurrencyInput) (*lambdaapi.Concurrency, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		f, ok := st.Functions[ref.name]
		if !ok {
			// Put names $LATEST, unlike Get and Delete
			// (test_function_concurrency_exceptions).
			return struct{}{}, errFunctionNotFound(ref.arn() + ":" + latest)
		}
		if st.unreserved(ref.name)-in.ReservedConcurrentExecutions < minUnreservedConcurrency {
			return struct{}{}, errUnreservedMinimum()
		}
		f = f.clone()
		f.Reserved = new(in.ReservedConcurrentExecutions)
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.arn())
	return &lambdaapi.Concurrency{ReservedConcurrentExecutions: new(in.ReservedConcurrentExecutions)}, nil
}

func (s *Service) getFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionConcurrencyInput) (*lambdaapi.GetFunctionConcurrencyOutput, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	return view(s, ref.scope, func(st *State) (*lambdaapi.GetFunctionConcurrencyOutput, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		out := &lambdaapi.GetFunctionConcurrencyOutput{}
		if f.Reserved != nil {
			out.ReservedConcurrentExecutions = new(*f.Reserved)
		}
		return out, nil
	})
}

func (s *Service) deleteFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteFunctionConcurrencyInput) (*lambdaapi.DeleteFunctionConcurrencyOutput, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return struct{}{}, err
		}
		f = f.clone()
		f.Reserved = nil
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.arn())
	return &lambdaapi.DeleteFunctionConcurrencyOutput{}, nil
}
