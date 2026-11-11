package kernel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// typedOptions returns one of each typed option, all for the input type In.
func typedOptions[In any]() map[string]kernel.BindOption {
	return map[string]kernel.BindOption{
		"WithValidator":        kernel.WithValidator(func(*In) []awserr.Violation { return nil }),
		"WithCheck":            kernel.WithCheck(func(*In) *awserr.Error { return nil }),
		"WithResources":        kernel.WithResources(func(*kernel.Call, *In) []aws.ARN { return nil }),
		"WithResourceARN":      kernel.WithResourceARN(func(*In) string { return "" }),
		"WithResourcesFor":     kernel.WithResourcesFor(nil, func(*kernel.Call, *In) []aws.ARN { return nil }),
		"WithItemCount":        kernel.WithItemCount(func(*In) int { return 0 }),
		"WithActions":          kernel.WithActions(func(*kernel.Call, *In) []registry.IAMAction { return nil }),
		"WithConditionKeys":    kernel.WithConditionKeys(func(*kernel.Call, *In) kernel.ConditionKeys { return nil }),
		"WithConditionKeysFor": kernel.WithConditionKeysFor(nil, func(*kernel.Call, *In) kernel.ConditionKeys { return nil }),
		"ResolveFirst":         kernel.ResolveFirst(func(context.Context, *kernel.Call, *In) *awserr.Error { return nil }),
		"BindOptions":          kernel.BindOptions(kernel.WithCheck(func(*In) *awserr.Error { return nil })),
	}
}

func sendMessage(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
	return &sqsapi.SendMessageOutput{}, nil
}

// wantOptionType fails t unless err holds an OptionType BindError.
func wantOptionType(t *testing.T, err error) {
	t.Helper()
	bind, ok := errors.AsType[*kernel.BindError](err)
	if !ok || bind.Problem != kernel.OptionType {
		t.Errorf("BindErr = %v, want an OptionType BindError", err)
	}
}

// A typed option is checked against the operation's input type: one built
// for that type binds, and one built for another is a bind mistake, at the
// bind site and through Declare alike.
func TestTypedOptionsMatchTheOperationsInputType(t *testing.T) {
	for name, opt := range typedOptions[sqsapi.SendMessageInput]() {
		t.Run(name+"/same input binds", func(t *testing.T) {
			k := newKernel(t)
			b := k.Binder(sqsapi.Service())
			kernel.Bind(b, sqsapi.OpSendMessage, sendMessage, registry.Full{}, opt)
			kernel.Declare(b, sqsapi.OpSendMessage, opt)
			if err := k.BindErr(); err != nil {
				t.Errorf("BindErr = %v, want nil", err)
			}
		})
	}
	for name, opt := range typedOptions[sqsapi.ReceiveMessageInput]() {
		t.Run(name+"/other input at Bind", func(t *testing.T) {
			k := newKernel(t)
			kernel.Bind(k.Binder(sqsapi.Service()), sqsapi.OpSendMessage, sendMessage, registry.Full{}, opt)
			wantOptionType(t, k.BindErr())
		})
		t.Run(name+"/other input at Declare", func(t *testing.T) {
			k := newKernel(t)
			b := k.Binder(sqsapi.Service())
			kernel.Bind(b, sqsapi.OpSendMessage, sendMessage, registry.Full{})
			if err := k.BindErr(); err != nil {
				t.Fatalf("BindErr after Bind = %v, want nil", err)
			}
			kernel.Declare(b, sqsapi.OpSendMessage, opt)
			wantOptionType(t, k.BindErr())
		})
	}
}
