package dynamodb_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// internalGate is a step 8 Authorizer that allows every client call and
// denies every internal call, recording each internal operation it is
// asked about.
type internalGate struct {
	mu    sync.Mutex
	asked []ref.OperationID
}

func (g *internalGate) Authorize(_ context.Context, c *kernel.Call, _ kernel.AuthzQuery) kernel.Verdict {
	if c.Mode() == nil {
		return kernel.Allowed{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, c.Op().ID)
	return kernel.Denied{Message: "internal calls are denied in this test"}
}

func (g *internalGate) questions() []ref.OperationID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ref.OperationID(nil), g.asked...)
}

// TTL deletes are DynamoDB's own service deletions (dynamodb-local.md 5.1),
// which no policy grants, so the sweep runs as the platform and step 8
// never asks about it: under an Authorizer that denies every internal call,
// an expired item is still deleted at the instant its attribute names.
func TestTTLSweepIsNeverAuthorized(t *testing.T) {
	gate := &internalGate{}
	store := newTableStore()
	runWithOptions(t, &fakeLocal{answer: store.serve}, []kerneltest.Option{kerneltest.Authorizer(gate)}, func(h *harness) {
		h.lockTable("Locks")
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"a"},"exp":{"N":"946684830"}}}`)
		h.settle()
		if _, err := h.Clock.Advance(30 * time.Second); err != nil {
			h.Fatal(err)
		}
		h.settle()
		if asked := gate.questions(); len(asked) != 0 {
			h.Errorf("step 8 was asked about %v, want nothing from the sweep", asked)
		}
		deletes := 0
		for _, f := range h.local.requests() {
			if f.Op == "DeleteItem" {
				deletes++
			}
		}
		if deletes != 1 {
			h.Errorf("Local received %d DeleteItem requests, want the sweep's one", deletes)
		}
	})
}
