package coverage

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// codec writes and reads a report, its sums in the tagged-union
// convention: a status is {"kind": "full"} or {"kind": "partial",
// "note": ...}, the overlays {"kind": "read", "entries": [...]} or
// {"kind": "not-carried"}.
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
	return jsonx.NewCodec(statuses, overlays), nil
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
