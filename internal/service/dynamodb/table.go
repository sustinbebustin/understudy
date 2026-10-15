package dynamodb

import (
	"crypto/sha256"
	"fmt"
	"maps"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Table is the shim's record of one table Local holds: what Local drops or
// answers wrongly about it (dynamodb-local.md 4.2, 6). The pointer members
// are optional settings, replaced whole and never changed in place, so a
// record copied out of the cell shares them safely; the maps are copied
// (clone).
type Table struct {
	// ID is the TableId Local omits.
	ID string `json:"id"`
	// Created is CreationDateTime, on the emulator clock.
	Created clock.Instant `json:"created"`
	// Billing is the billing mode and when the table last became
	// PAY_PER_REQUEST; never nil.
	Billing Billing `json:"billing"`
	// Tags are the table's tags, which Local drops.
	Tags map[string]string `json:"tags,omitempty"`
	// TTL is the time-to-live specification; nil while TTL is disabled.
	// It never reaches Local, so Local's wall-clock sweeper stays off.
	TTL *TTLSpec `json:"ttl,omitempty"`
	// PITR is point-in-time recovery; nil while it is disabled.
	PITR *PITR `json:"pitr,omitempty"`
	// Class is the table class a CreateTable or UpdateTable set; nil when
	// none did, which reads as STANDARD.
	Class *Class `json:"class,omitempty"`
	// SSE is encryption with a KMS key; nil for the AWS owned key, which
	// AWS does not describe.
	SSE *SSE `json:"sse,omitempty"`
	// Warm is the warm throughput a CreateTable or UpdateTable set; nil
	// for AWS's default.
	Warm *Warm `json:"warm,omitempty"`
	// IndexWarm is the warm throughput a CreateTable or UpdateTable set on
	// each global secondary index, by index name; an index without an
	// entry has AWS's default.
	IndexWarm map[string]Warm `json:"indexWarm,omitempty"`
	// Throughput is the table's provisioned throughput; nil while the
	// table has none the shim saw set, as while it is PAY_PER_REQUEST.
	Throughput *Throughput `json:"throughput,omitempty"`
	// IndexThroughput is the provisioned throughput of each global
	// secondary index that has one, by index name.
	IndexThroughput map[string]Throughput `json:"indexThroughput,omitempty"`
}

// Billing is a table's billing mode as the shim saw it set. AWS reports
// LastUpdateToPayPerRequestDateTime, the last switch to PAY_PER_REQUEST,
// in either mode, so a switch back to PROVISIONED keeps the instant.
//
//sumtype:decl
type Billing interface {
	isBilling()
	// lastOnDemand is when the table last became PAY_PER_REQUEST; false
	// when the shim knows of no such instant.
	lastOnDemand() (clock.Instant, bool)
}

// Provisioned is PROVISIONED billing.
type Provisioned struct {
	// LastOnDemand is when the table last became PAY_PER_REQUEST; zero when
	// it never did.
	LastOnDemand clock.Instant `json:"lastOnDemand,omitzero"`
}

// OnDemand is PAY_PER_REQUEST billing since Since.
type OnDemand struct {
	Since clock.Instant `json:"since"`
}

func (Provisioned) isBilling() {}
func (OnDemand) isBilling()    {}

func (b Provisioned) lastOnDemand() (clock.Instant, bool) {
	return b.LastOnDemand, !b.LastOnDemand.IsZero()
}
func (b OnDemand) lastOnDemand() (clock.Instant, bool) { return b.Since, !b.Since.IsZero() }

// billed is the billing after an UpdateTable sets mode at now on top of
// was: becoming PAY_PER_REQUEST starts a new instant, staying keeps it,
// and becoming PROVISIONED keeps the last one.
func billed(was Billing, mode dynamodbapi.BillingMode, now clock.Instant) Billing {
	switch mode {
	case dynamodbapi.BillingModePayPerRequest:
		if b, ok := was.(OnDemand); ok {
			return b
		}
		return OnDemand{Since: now}
	case dynamodbapi.BillingModeProvisioned:
		last := Provisioned{}
		last.LastOnDemand, _ = was.lastOnDemand()
		return last
	}
	return was
}

// adoptedBilling is the billing of a table Local describes in summary,
// with Local's own instant of its last switch to PAY_PER_REQUEST, as an
// adopted record keeps Local's CreationDateTime.
func adoptedBilling(summary *dynamodbapi.BillingModeSummary, created clock.Instant) Billing {
	var last clock.Instant
	if summary != nil && summary.LastUpdateToPayPerRequestDateTime != nil {
		last = clock.FromWire(*summary.LastUpdateToPayPerRequestDateTime)
	}
	if summary != nil && summary.BillingMode != nil && *summary.BillingMode == dynamodbapi.BillingModePayPerRequest {
		if last.IsZero() {
			last = created
		}
		return OnDemand{Since: last}
	}
	return Provisioned{LastOnDemand: last}
}

// Throughput is provisioned throughput as a request set it, and when it
// last went up and down on the emulator clock, which Local stamps from the
// wall clock instead.
type Throughput struct {
	Reads  int64 `json:"reads"`
	Writes int64 `json:"writes"`
	// Increased is LastIncreaseDateTime; zero when it never went up.
	Increased clock.Instant `json:"increased,omitzero"`
	// Decreased is LastDecreaseDateTime; zero when it never went down.
	Decreased clock.Instant `json:"decreased,omitzero"`
}

// provisioned is the throughput p sets at now on top of was: it went up
// when either capacity rises and down when either falls. Throughput the
// shim never saw (was nil) neither rises nor falls.
func provisioned(was *Throughput, p *dynamodbapi.ProvisionedThroughput, now clock.Instant) Throughput {
	next := Throughput{Reads: p.ReadCapacityUnits, Writes: p.WriteCapacityUnits}
	if was == nil {
		return next
	}
	next.Increased, next.Decreased = was.Increased, was.Decreased
	if next.Reads > was.Reads || next.Writes > was.Writes {
		next.Increased = now
	}
	if next.Reads < was.Reads || next.Writes < was.Writes {
		next.Decreased = now
	}
	return next
}

// TTLSpec is an enabled time-to-live specification.
type TTLSpec struct {
	// Attribute holds each item's expiry in Unix epoch seconds.
	Attribute string `json:"attribute"`
}

// PITR is enabled point-in-time recovery.
type PITR struct {
	// Since is when it was enabled, the earliest restorable instant.
	Since clock.Instant `json:"since"`
	// Days is RecoveryPeriodInDays.
	Days int32 `json:"days"`
}

// Class is a table class set on the table.
type Class struct {
	Name dynamodbapi.TableClass `json:"name"`
	// Updated is LastUpdateDateTime: when an UpdateTable last set it; zero
	// for the class the table was created with.
	Updated clock.Instant `json:"updated,omitzero"`
}

// Warm is configured warm throughput.
type Warm struct {
	Reads  int64 `json:"reads"`
	Writes int64 `json:"writes"`
}

// clone copies t so that changing the copy's tags or index settings
// leaves t alone.
func (t Table) clone() Table {
	t.Tags = maps.Clone(t.Tags)
	t.IndexWarm = maps.Clone(t.IndexWarm)
	t.IndexThroughput = maps.Clone(t.IndexThroughput)
	return t
}

// newTable is the record of the table in, created by c: everything
// CreateTable says that Local drops, stamped with c's admission instant.
// sse is the encryption the request enables, resolved through KMS; nil
// for the AWS owned key.
func newTable(c *kernel.Call, in *dynamodbapi.CreateTableInput, sse *SSE) Table {
	t := Table{ID: tableID(c.ID()), Created: c.Admitted(), SSE: sse, Billing: Provisioned{}}
	onDemand := in.BillingMode != nil && *in.BillingMode == dynamodbapi.BillingModePayPerRequest
	if onDemand {
		t.Billing = OnDemand{Since: t.Created}
	}
	if len(in.Tags) > 0 {
		t.Tags = make(map[string]string, len(in.Tags))
		for _, tag := range in.Tags {
			t.Tags[tag.Key] = tag.Value
		}
	}
	if in.TableClass != nil {
		t.Class = &Class{Name: *in.TableClass}
	}
	t.Warm = warmed(nil, in.WarmThroughput)
	for _, g := range in.GlobalSecondaryIndexes {
		t.IndexWarm = indexWarmed(t.IndexWarm, g.IndexName, g.WarmThroughput)
	}
	if onDemand {
		return t
	}
	if in.ProvisionedThroughput != nil {
		t.Throughput = new(provisioned(nil, in.ProvisionedThroughput, t.Created))
	}
	for _, g := range in.GlobalSecondaryIndexes {
		if g.ProvisionedThroughput != nil {
			t.IndexThroughput = withIndex(t.IndexThroughput, g.IndexName, provisioned(nil, g.ProvisionedThroughput, t.Created))
		}
	}
	return t
}

// tableID derives a TableId from the request id of the CreateTable that
// made the table, so seeded runs mint the same ids and no two tables share
// one, without drawing from another identifier's stream.
func tableID(request ids.RequestID) string {
	return uuidOf(sha256.Sum256([]byte("dynamodb-table-id\x00" + string(request))))
}

// adoptedID derives the TableId of an adopted table from its path, so the
// same table gets the same id however often it is adopted.
func adoptedID(k tableKey) string {
	return uuidOf(sha256.Sum256([]byte("dynamodb-adopted-table-id\x00" + k.path())))
}

// uuidOf formats a hash as a version 4 UUID.
func uuidOf(h [sha256.Size]byte) string {
	h[6] = h[6]&0x0f | 0x40
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// withIndex returns m, a record's own IndexThroughput or IndexWarm, with
// the setting of the index name set to v.
func withIndex[V any](m map[string]V, name string, v V) map[string]V {
	if m == nil {
		m = map[string]V{}
	}
	m[name] = v
	return m
}

// warmed is the warm throughput after a request sets in on top of was; a
// member the request leaves out keeps its value.
func warmed(was *Warm, in *dynamodbapi.WarmThroughput) *Warm {
	if in == nil || (in.ReadUnitsPerSecond == nil && in.WriteUnitsPerSecond == nil) {
		return was
	}
	w := Warm{Reads: defaultWarmReads, Writes: defaultWarmWrites}
	if was != nil {
		w = *was
	}
	if in.ReadUnitsPerSecond != nil {
		w.Reads = *in.ReadUnitsPerSecond
	}
	if in.WriteUnitsPerSecond != nil {
		w.Writes = *in.WriteUnitsPerSecond
	}
	return &w
}

// updated is t after an UpdateTable in admitted at now: the members the
// shim owns, when the table last became PAY_PER_REQUEST, and its
// provisioned throughput. sse is the encryption the request enables,
// resolved through KMS, when it enables one (sseKMS).
func (t Table) updated(in *dynamodbapi.UpdateTableInput, now clock.Instant, sse *SSE) Table {
	t = t.clone()
	if in.TableClass != nil {
		t.Class = &Class{Name: *in.TableClass, Updated: now}
	}
	switch sseChangeOf(in.SSESpecification) {
	case sseOff:
		t.SSE = nil
	case sseKMS:
		t.SSE = sse
	case sseKept:
	}
	t.Warm = warmed(t.Warm, in.WarmThroughput)
	if in.BillingMode != nil {
		t.Billing = billed(t.Billing, *in.BillingMode, now)
		if *in.BillingMode == dynamodbapi.BillingModePayPerRequest {
			t.Throughput, t.IndexThroughput = nil, nil
		}
	}
	if in.ProvisionedThroughput != nil {
		t.Throughput = new(provisioned(t.Throughput, in.ProvisionedThroughput, now))
	}
	return t.indexesUpdated(in.GlobalSecondaryIndexUpdates, now)
}

// indexesUpdated is t after updates, an UpdateTable's
// GlobalSecondaryIndexUpdates admitted at now: the provisioned and warm
// throughput each creates or changes, and the settings of each index it
// deletes dropped. t is the caller's own copy.
func (t Table) indexesUpdated(updates []dynamodbapi.GlobalSecondaryIndexUpdate, now clock.Instant) Table {
	for _, u := range updates {
		switch {
		case u.Create != nil:
			if u.Create.ProvisionedThroughput != nil {
				t.IndexThroughput = withIndex(t.IndexThroughput, u.Create.IndexName, provisioned(nil, u.Create.ProvisionedThroughput, now))
			}
			t.IndexWarm = indexWarmed(t.IndexWarm, u.Create.IndexName, u.Create.WarmThroughput)
		case u.Update != nil:
			if u.Update.ProvisionedThroughput != nil {
				var was *Throughput
				if p, ok := t.IndexThroughput[u.Update.IndexName]; ok {
					was = &p
				}
				t.IndexThroughput = withIndex(t.IndexThroughput, u.Update.IndexName, provisioned(was, u.Update.ProvisionedThroughput, now))
			}
			t.IndexWarm = indexWarmed(t.IndexWarm, u.Update.IndexName, u.Update.WarmThroughput)
		case u.Delete != nil:
			delete(t.IndexThroughput, u.Delete.IndexName)
			delete(t.IndexWarm, u.Delete.IndexName)
		}
	}
	if len(t.IndexThroughput) == 0 {
		t.IndexThroughput = nil
	}
	if len(t.IndexWarm) == 0 {
		t.IndexWarm = nil
	}
	return t
}

// indexWarmed returns m, a record's own IndexWarm, after a request sets in
// on the index name; a member the request leaves out keeps its value.
func indexWarmed(m map[string]Warm, name string, in *dynamodbapi.WarmThroughput) map[string]Warm {
	var was *Warm
	if w, ok := m[name]; ok {
		was = &w
	}
	w := warmed(was, in)
	if w == nil {
		return m
	}
	return withIndex(m, name, *w)
}
