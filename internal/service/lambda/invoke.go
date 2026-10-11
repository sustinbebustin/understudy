package lambda

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Drain stops the execution environments and pollers of scope before a
// reset or snapshot load replaces its state. This build runs neither, so
// there is nothing to stop and Drain returns at once.
func (*Service) Drain(context.Context, kernel.ResetScope) error { return nil }

// hotReloadAdvisory runs after a committed UpdateFunctionCode of the
// function ref names. It is where an update to a function whose code a
// hot-reload overlay serves records journal.AdvisoryShadowedCodeUpdate,
// since the overlay keeps precedence over the stored code. This build has
// no hot reload, so no update is shadowed and there is nothing to record.
func (*Service) hotReloadAdvisory(*kernel.Call, functionRef) {}
