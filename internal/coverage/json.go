package coverage

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// codec writes and reads a report, its sums in the tagged-union
// convention: a status is {"kind": "full"} or {"kind": "partial",
// "note": ...}, the overlays {"kind": "read", "entries": [...]} or
// {"kind": "not-carried"}, a switch's settles {"kind": "scenario", "step":
// "sqs/R01#03-recv-after-6s"} or {"kind": "none", "reason": ...}, the
// parity evidence {"kind": "read", "scenarios": [...]} or {"kind":
// "not-carried"}, and within it a step's outcome, a claim's target and its
// result as internal/parity/baseline.json spells them, so ParseParity
// reads that file with the same codec.
func codec() (jsonx.Codec, error) {
	statuses, err := jsonx.NewUnion("coverage.Status",
		jsonx.Variant[Status, Full]("full"),
		jsonx.Variant[Status, StoredOnly]("stored-only"),
		jsonx.Variant[Status, Partial]("partial"),
		jsonx.Variant[Status, Unbound]("unbound"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	overlays, err := jsonx.NewUnion("coverage.Overlays",
		jsonx.Variant[Overlays, OverlaysRead]("read"),
		jsonx.Variant[Overlays, OverlaysNotCarried]("not-carried"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	settles, err := jsonx.NewUnion("fidelity.Settles",
		jsonx.Variant[fidelity.Settles, fidelity.SettledBy]("scenario"),
		jsonx.Variant[fidelity.Settles, fidelity.NoScenario]("none"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	parity, err := jsonx.NewUnion("coverage.Parity",
		jsonx.Variant[Parity, ParityRead]("read"),
		jsonx.Variant[Parity, ParityNotCarried]("not-carried"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	baseline, err := parityCodec()
	if err != nil {
		return jsonx.Codec{}, err
	}
	return jsonx.NewCodec(append([]jsonx.Sum{statuses, overlays, settles, parity}, baseline...)...), nil
}

// parityCodec is the sums of internal/parity/baseline.json: a step's
// outcome, a claim's target and a claim's result.
func parityCodec() ([]jsonx.Sum, error) {
	outcomes, err := jsonx.NewUnion("coverage.ParityOutcome",
		jsonx.Variant[ParityOutcome, StepMatch]("match"),
		jsonx.Variant[ParityOutcome, StepDiffers]("differs"),
		jsonx.Variant[ParityOutcome, StepReconstructed]("reconstructed"),
		jsonx.Variant[ParityOutcome, StepSkipped]("skipped"),
		jsonx.Variant[ParityOutcome, StepPending]("pending"),
	)
	if err != nil {
		return nil, err
	}
	targets, err := jsonx.NewUnion("coverage.ClaimTarget",
		jsonx.Variant[ClaimTarget, TargetSwitch]("switch"),
		jsonx.Variant[ClaimTarget, TargetOverlay]("overlay"),
	)
	if err != nil {
		return nil, err
	}
	results, err := jsonx.NewUnion("coverage.ClaimResult",
		jsonx.Variant[ClaimResult, ClaimSettled]("settled"),
		jsonx.Variant[ClaimResult, ClaimInsensitive]("insensitive"),
		jsonx.Variant[ClaimResult, ClaimContradicted]("contradicted"),
		jsonx.Variant[ClaimResult, ClaimOpen]("open"),
	)
	if err != nil {
		return nil, err
	}
	return []jsonx.Sum{outcomes, targets, results}, nil
}

// JSON returns the report as JSON, members sorted by name.
func (r Report) JSON() ([]byte, error) {
	c, err := codec()
	if err != nil {
		return nil, err
	}
	data, err := c.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("coverage: encoding the report: %w", err)
	}
	return data, nil
}

// ParseJSON reads a report JSON wrote.
func ParseJSON(data []byte) (Report, error) {
	c, err := codec()
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := c.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("coverage: reading a report: %w", err)
	}
	return r, nil
}
