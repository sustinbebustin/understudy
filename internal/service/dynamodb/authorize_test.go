package dynamodb_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// scanGate is a fake Authorizer that records every question about a call
// of op and answers every question with verdict.
type scanGate struct {
	mu      sync.Mutex
	op      ref.OperationID
	verdict kernel.Verdict
	asked   []kernel.AuthzQuery
}

func (g *scanGate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.Op().ID == g.op {
		g.asked = append(g.asked, q)
	}
	return g.verdict
}

func (g *scanGate) questions() []kernel.AuthzQuery {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]kernel.AuthzQuery(nil), g.asked...)
}

// Scan's generated list holds dynamodb:ReadDataForReplication beside
// dynamodb:Scan (the Service Reference's authorized actions), but a Scan
// needs only dynamodb:Scan, so step 8 asks about it alone: the binding
// declares nothing more, and the kernel never asks about the whole list.
func TestStep8AsksAboutScanAlone(t *testing.T) {
	gate := &scanGate{op: dynamodbapi.OpScan.Descriptor().ID, verdict: kernel.Allowed{}}
	runWithOptions(t, &fakeLocal{}, []kerneltest.Option{kerneltest.Authorizer(gate)}, func(h *harness) {
		h.call("Scan", `{"TableName":"Locks"}`)
		got := gate.questions()
		if len(got) != 1 || len(got[0].Pairs) == 0 {
			h.Fatalf("the Authorizer was asked %+v, want one question about the Scan", got)
		}
		scan := registry.IAMAction{Prefix: "dynamodb", Name: "Scan"}
		for _, p := range got[0].Pairs {
			if p.Action != scan {
				h.Errorf("step 8 asked about %s, want dynamodb:Scan only", p.Action)
			}
		}
	})
}

// DynamoDB is a proxy to DynamoDB Local, so enforcement decides before
// forwarding (iam-enforcement-surfaces.md 12.1): a denied call never reaches
// Local and answers DynamoDB's denial identity. That identity is inferred
// (switch dynamodb.denial.type, case IAM-ENF-04), so it is read from the
// descriptor.
func TestADeniedCallIsNotForwardedToLocal(t *testing.T) {
	gate := &scanGate{op: dynamodbapi.OpScan.Descriptor().ID, verdict: kernel.Denied{Message: "User: u is not authorized to perform: dynamodb:Scan"}}
	runWithOptions(t, &fakeLocal{}, []kerneltest.Option{kerneltest.Authorizer(gate)}, func(h *harness) {
		denial := dynamodbapi.Service().Denial.Identity
		if denial.Shape == "" {
			h.Fatal("dynamodb names no denial identity")
		}
		got := h.call("Scan", `{"TableName":"Locks"}`)
		if e := parseError(h, got.Body); got.Status != denial.Status || e.Type != denial.JSONType {
			h.Errorf("Scan = %d %s, want %d with __type %s", got.Status, got.Body, denial.Status, denial.JSONType)
		}
		if sent := h.local.requests(); len(sent) != 0 {
			h.Errorf("Local received %+v for a denied call, want nothing", sent)
		}
	})
}
