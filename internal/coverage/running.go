package coverage

import (
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
)

// SwitchSource is a wired service that declares fidelity switches; every
// service exposes Switches (ARCHITECTURE.md 3.4).
type SwitchSource interface {
	Switches() []fidelity.Entry
}

// Running is the input of a running server's report: its kernel's
// registry and bindings, the protocol-test baseline, skip list and
// ungenerated cases built into the binary, and the switches of the wired
// services. The binary does not carry the overlay files, so the report
// says where they are listed.
func Running(k *kernel.Kernel, services ...SwitchSource) (Input, error) {
	baseline, err := protocoltest.CheckedInBaseline()
	if err != nil {
		return Input{}, err
	}
	skips, err := protocoltest.CheckedInSkips()
	if err != nil {
		return Input{}, err
	}
	in := Input{
		Registry: k.Registry(), Bindings: k.Bindings(), Baseline: baseline, Skips: skips,
		Ungenerated: protocoltest.UngeneratedCases(), Overlays: OverlaysNotCarried{},
	}
	for _, s := range services {
		in.Switches = append(in.Switches, s.Switches()...)
	}
	return in, nil
}
