package dynamodb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Table is the shim's record of one table Local holds: what Local drops or
// answers wrongly about it (dynamodb-local.md 4.2, 6). The pointer members
// are optional settings, replaced whole and never changed in place, so a
// record copied out of the cell shares them safely; Tags is copied (clone).
type Table struct {
	// ID is the TableId Local omits.
	ID string `json:"id"`
	// Created is CreationDateTime, on the emulator clock.
	Created clock.Instant `json:"created"`
	// OnDemandSince is LastUpdateToPayPerRequestDateTime: when the table
	// last became PAY_PER_REQUEST; zero while it is PROVISIONED.
	OnDemandSince clock.Instant `json:"onDemandSince,omitzero"`
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
	// Throughput is the table's provisioned throughput; nil while the
	// table has none the shim saw set, as while it is PAY_PER_REQUEST.
	Throughput *Throughput `json:"throughput,omitempty"`
	// IndexThroughput is the provisioned throughput of each global
	// secondary index that has one, by index name.
	IndexThroughput map[string]Throughput `json:"indexThroughput,omitempty"`
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

// SSE is server-side encryption with a KMS key.
type SSE struct {
	KMSKeyARN string `json:"kmsKeyArn"`
}

// Warm is configured warm throughput.
type Warm struct {
	Reads  int64 `json:"reads"`
	Writes int64 `json:"writes"`
}

// tableKey names one table: the TTL sweep timer's id and the snapshot
// section of its items are its path.
type tableKey struct {
	scope aws.Scope
	table string
}

// path is "<account>/<region>/<table>". A table name holds no slash, so
// the last one splits it.
func (k tableKey) path() string { return store.ScopeKeys().Encode(k.scope) + "/" + k.table }

// parseTableKey reads a path back.
func parseTableKey(path string) (tableKey, error) {
	i := strings.LastIndexByte(path, '/')
	if i < 0 || i == len(path)-1 {
		return tableKey{}, &TableKeyError{Path: path}
	}
	scope, err := store.ScopeKeys().Decode(path[:i])
	if err != nil {
		return tableKey{}, &TableKeyError{Path: path, Err: err}
	}
	return tableKey{scope: scope, table: path[i+1:]}, nil
}

// TableKeyError is a timer id or section path that names no table.
type TableKeyError struct {
	Path string
	Err  error
}

func (e *TableKeyError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%q names no table: the layout is <account>/<region>/<table>", e.Path)
	}
	return fmt.Sprintf("%q names no table: the layout is <account>/<region>/<table>: %v", e.Path, e.Err)
}

func (e *TableKeyError) Unwrap() error { return e.Err }

// clone copies t so that changing the copy's tags or index throughput
// leaves t alone.
func (t Table) clone() Table {
	t.Tags = maps.Clone(t.Tags)
	t.IndexThroughput = maps.Clone(t.IndexThroughput)
	return t
}

// newTable is the record of the table in, created by c: everything
// CreateTable says that Local drops, stamped with c's admission instant.
// sse is the encryption the request enables, resolved through KMS; nil
// for the AWS owned key.
func newTable(c *kernel.Call, in *dynamodbapi.CreateTableInput, sse *SSE) Table {
	t := Table{ID: tableID(c.ID()), Created: c.Admitted(), SSE: sse}
	onDemand := in.BillingMode != nil && *in.BillingMode == dynamodbapi.BillingModePayPerRequest
	if onDemand {
		t.OnDemandSince = t.Created
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

// creation is the record of the table a CreateTable in creates, after
// refusing what AWS refuses and Local accepts: tags past AWS's limits
// (checkTags) and a KMS key KMS does not find (encryption).
func (s *Service) creation(ctx context.Context, c *kernel.Call, in *dynamodbapi.CreateTableInput) (Table, *awserr.Error) {
	if err := checkTags(nil, in.Tags); err != nil {
		return Table{}, err
	}
	var sse *SSE
	if enablesSSE(in.SSESpecification) {
		var err *awserr.Error
		if sse, err = s.encryption(ctx, c, in.SSESpecification.KMSMasterKeyId); err != nil {
			return Table{}, err
		}
	}
	return newTable(c, in, sse), nil
}

// recorded returns the shim's record of the table name in c's scope. A
// table Local holds that the shim never saw created (one Local kept under
// --persist without the archive, written in the last 30 s before a crash)
// is adopted: its record is built from Local's description, with Local's
// CreationDateTime and a TableId derived from the table's path, so the
// shim operations answer for it as for any table. missing is the answer
// when neither holds it, and when the scope holds only a case twin, which
// Local would describe in its place.
func (s *Service) recorded(ctx context.Context, c *kernel.Call, name string, missing *awserr.Error) (Table, *awserr.Error) {
	if t, ok := s.lookup(c.Scope(), name); ok {
		return t, nil
	}
	if cell, ok := s.cells.Lookup(c.Scope()); ok && store.Read(cell, func(st *State) bool { return st.onlyTwinOf(name) }) {
		return Table{}, missing
	}
	describe := dynamodbapi.OpDescribeTableRaw.Descriptor()
	body, err := encodeInput(describe, &dynamodbapi.DescribeTableInput{TableName: name})
	if err != nil {
		return Table{}, err
	}
	endpoint, err := s.ready(ctx)
	if err != nil {
		return Table{}, err
	}
	resp, err := s.send(ctx, endpoint, exchange{scope: c.Scope(), op: describe.ID, at: c.Admitted()}, body)
	if err != nil {
		return Table{}, err
	}
	var out dynamodbapi.DescribeTableOutput
	if derr := decodeAnswer(describe, resp, &out); derr != nil {
		if le, ok := errors.AsType[*LocalError](derr); ok && le.is("ResourceNotFoundException") {
			return Table{}, missing
		}
		return Table{}, awserr.Internalf(fmt.Errorf("asking DynamoDB Local whether it holds table %s, which the shim does not record: %w", name, derr))
	}
	if out.Table == nil || out.Table.TableName == nil || *out.Table.TableName != name {
		return Table{}, missing
	}
	k := tableKey{scope: c.Scope(), table: name}
	adopted := Table{ID: adoptedID(k)}
	if out.Table.CreationDateTime != nil {
		adopted.Created = clock.FromWire(*out.Table.CreationDateTime)
	}
	return kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (Table, *awserr.Error) {
		if t, ok := st.Tables[name]; ok {
			tx.Unchanged()
			return t.clone(), nil
		}
		st.Tables[name] = adopted
		return adopted.clone(), nil
	})
}

// adoptedID derives the TableId of an adopted table from its path, so the
// same table gets the same id however often it is adopted.
func adoptedID(k tableKey) string {
	return uuidOf(sha256.Sum256([]byte("dynamodb-adopted-table-id\x00" + k.path())))
}

// withIndex returns m, a record's own IndexThroughput, with the
// throughput of the index name set to p.
func withIndex(m map[string]Throughput, name string, p Throughput) map[string]Throughput {
	if m == nil {
		m = map[string]Throughput{}
	}
	m[name] = p
	return m
}

// enablesSSE reports whether a CreateTable's SSE specification turns on
// encryption with a KMS key.
func enablesSSE(sse *dynamodbapi.SSESpecification) bool {
	return sse != nil && sse.Enabled != nil && *sse.Enabled
}

// sseChange is what an UpdateTable's SSE specification does: nothing,
// switch to the AWS owned key, or encrypt with a KMS key.
type sseChange int

const (
	sseKept sseChange = iota
	sseOff
	sseKMS
)

func sseChangeOf(sse *dynamodbapi.SSESpecification) sseChange {
	switch {
	case sse == nil:
		return sseKept
	case sse.Enabled != nil && !*sse.Enabled:
		return sseOff
	case sse.Enabled != nil || sse.KMSMasterKeyId != nil:
		return sseKMS
	}
	return sseKept
}

// tableID derives a TableId from the request id of the CreateTable that
// made the table, so seeded runs mint the same ids and no two tables share
// one, without drawing from another identifier's stream.
func tableID(request ids.RequestID) string {
	return uuidOf(sha256.Sum256([]byte("dynamodb-table-id\x00" + string(request))))
}

// uuidOf formats a hash as a version 4 UUID.
func uuidOf(h [sha256.Size]byte) string {
	h[6] = h[6]&0x0f | 0x40
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// managedKey is the alias of the AWS managed key DynamoDB encrypts with
// when an SSE specification names no key.
const managedKey = "alias/aws/dynamodb"

// encryption is the encryption an SSE specification naming key enables:
// the ARN of the key KMS resolves it to, as AWS's KMSMasterKeyArn names
// the key, never an alias. No key is the AWS managed key, which KMS mints
// behind alias/aws/dynamodb on first use, so the Terraform provider's
// comparison with DescribeKey of that alias matches (tf-storage.md 3.2).
// The DescribeKey runs as forward access charged to the caller. A key KMS
// does not find is refused as DynamoDB refuses it; a server without KMS
// bound describes the key by kmsKeyARN instead.
func (s *Service) encryption(ctx context.Context, c *kernel.Call, key *string) (*SSE, *awserr.Error) {
	id := managedKey
	if key != nil && *key != "" {
		id = *key
	}
	describe := kmsapi.OpDescribeKey
	if _, modeled := s.kernel.Registry().Operation(describe.Descriptor().ID); !modeled {
		return &SSE{KMSKeyARN: kmsKeyARN(c.Scope(), key)}, nil
	}
	out, err := kernel.Invoke(ctx, s.kernel, c, describe, kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.KMSForward},
		&kmsapi.DescribeKeyInput{KeyId: id})
	switch {
	case err == nil && out.KeyMetadata != nil && out.KeyMetadata.Arn != nil:
		return &SSE{KMSKeyARN: *out.KeyMetadata.Arn}, nil
	case err == nil:
		return nil, awserr.Internalf(&KeyARNError{Key: id})
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape:
		return nil, errKeyNotFound(id)
	case err.ID.Shape == awserr.NotImplemented().Shape:
		return &SSE{KMSKeyARN: kmsKeyARN(c.Scope(), key)}, nil
	}
	return nil, err
}

// KeyARNError is a DescribeKey answer without the key's ARN.
type KeyARNError struct{ Key string }

func (e *KeyARNError) Error() string {
	return fmt.Sprintf("KMS answered DescribeKey of %q without KeyMetadata.Arn, so the table's KMSMasterKeyArn is unknown; nothing was created or changed", e.Key)
}

// errKeyNotFound refuses an SSE specification naming a key KMS does not
// find. The text is UNVERIFIED against AWS.
func errKeyNotFound(key string) *awserr.Error {
	return dynamodbapi.ErrValidationException("Invalid KMSMasterKeyId: KMS key " + key + " was not found")
}

// kmsKeyARN describes an SSE specification's key without KMS, for a server
// that does not bind it: an ARN as given, an alias or key id as its ARN in
// scope, and no key as the AWS managed key's alias.
func kmsKeyARN(scope aws.Scope, key *string) string {
	resource := managedKey
	switch {
	case key == nil || *key == "":
	case strings.HasPrefix(*key, "arn:"):
		return *key
	case strings.HasPrefix(*key, "alias/"):
		resource = *key
	default:
		resource = "key/" + *key
	}
	return aws.ARN{Partition: scope.Partition.String(), Service: "kms", Region: scope.Region.String(), Account: scope.Account.String(), Resource: resource}.String()
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
		switch *in.BillingMode {
		case dynamodbapi.BillingModePayPerRequest:
			if t.OnDemandSince.IsZero() {
				t.OnDemandSince = now
			}
			t.Throughput, t.IndexThroughput = nil, nil
		case dynamodbapi.BillingModeProvisioned:
			t.OnDemandSince = clock.Instant{}
		}
	}
	if in.ProvisionedThroughput != nil {
		t.Throughput = new(provisioned(t.Throughput, in.ProvisionedThroughput, now))
	}
	for _, u := range in.GlobalSecondaryIndexUpdates {
		switch {
		case u.Create != nil && u.Create.ProvisionedThroughput != nil:
			t.IndexThroughput = withIndex(t.IndexThroughput, u.Create.IndexName, provisioned(nil, u.Create.ProvisionedThroughput, now))
		case u.Update != nil && u.Update.ProvisionedThroughput != nil:
			var was *Throughput
			if p, ok := t.IndexThroughput[u.Update.IndexName]; ok {
				was = &p
			}
			t.IndexThroughput = withIndex(t.IndexThroughput, u.Update.IndexName, provisioned(was, u.Update.ProvisionedThroughput, now))
		case u.Delete != nil:
			delete(t.IndexThroughput, u.Delete.IndexName)
		}
	}
	if len(t.IndexThroughput) == 0 {
		t.IndexThroughput = nil
	}
	return t
}

// shimOnly reports whether an UpdateTable changes nothing but what the shim
// owns: SSESpecification, TableClass and WarmThroughput, which Local
// refuses alone with "Nothing to update" (dynamodb-local.md 4.2). Every
// other member of UpdateTableInput is listed, so a member the model adds
// later is forwarded.
func shimOnly(in *dynamodbapi.UpdateTableInput) bool {
	owned := in.SSESpecification != nil || in.TableClass != nil || in.WarmThroughput != nil
	others := len(in.AttributeDefinitions) > 0 || in.BillingMode != nil || in.ProvisionedThroughput != nil ||
		len(in.GlobalSecondaryIndexUpdates) > 0 || in.StreamSpecification != nil || len(in.ReplicaUpdates) > 0 ||
		in.DeletionProtectionEnabled != nil || in.MultiRegionConsistency != nil || len(in.GlobalTableWitnessUpdates) > 0 ||
		in.OnDemandThroughput != nil || in.GlobalTableSettingsReplicationMode != nil || len(in.VectorIndexUpdates) > 0
	return owned && !others
}

// updateTable applies an UpdateTable. Local holds the table's schema and
// throughput, the shim the members it owns: an update of only those is
// answered here with Local's description, which Local would refuse; any
// other update is forwarded as the client sent it, and the shim's members
// it carries are recorded once Local accepts it. A table the shim does not
// record is Local's to answer.
func (s *Service) updateTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.UpdateTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	name := tableName(in.TableName)
	a := tableAnswer(in.TableName)
	current, known := s.lookup(c.Scope(), name)
	if !known {
		a.shim = &shimmed{}
		return s.forward(ctx, c, in, w, a, []string{in.TableName})
	}
	var sse *SSE
	if sseChangeOf(in.SSESpecification) == sseKMS {
		if err := preForward(c); err != nil {
			return kernel.RawResponse{}, err
		}
		var err *awserr.Error
		if sse, err = s.encryption(ctx, c, in.SSESpecification.KMSMasterKeyId); err != nil {
			return kernel.RawResponse{}, err
		}
	}
	// The answer and the record carry the same instant, c's admission, so
	// the UpdateTable answer and the next DescribeTable agree.
	next := current.updated(in, c.Admitted(), sse)
	a.shim = &shimmed{table: &next}
	var resp kernel.RawResponse
	var err *awserr.Error
	if shimOnly(in) {
		a.shim.under = "TableDescription"
		resp, err = s.describeLocally(ctx, c, in.TableName, a)
	} else {
		resp, err = s.forward(ctx, c, in, w, a, []string{in.TableName})
	}
	if err != nil || resp.Status != http.StatusOK {
		return resp, err
	}
	_, err = s.alter(c, name, dynamodbapi.ErrResourceNotFoundException(awsNotFound(name)), func(t *Table, _ clock.Instant) *awserr.Error {
		*t = t.updated(in, c.Admitted(), sse)
		return nil
	})
	return resp, err
}

// describeLocally answers c with Local's description of table, completed
// and rewritten as a says, after the checks a forwarded call gets.
func (s *Service) describeLocally(ctx context.Context, c *kernel.Call, table string, a answer) (kernel.RawResponse, *awserr.Error) {
	if err := preForward(c); err != nil {
		return kernel.RawResponse{}, err
	}
	if err := inScope(c.Scope(), []string{table}, a); err != nil {
		return kernel.RawResponse{}, err
	}
	describe := dynamodbapi.OpDescribeTableRaw.Descriptor()
	body, err := encodeInput(describe, &dynamodbapi.DescribeTableInput{TableName: table})
	if err != nil {
		return kernel.RawResponse{}, err
	}
	endpoint, err := s.ready(ctx)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	local, err := s.send(ctx, endpoint, exchange{scope: c.Scope(), op: describe.ID, at: c.Admitted()}, body)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	return rewrite(c, a, local)
}

// alter changes the record of the table name in c's scope with fn, under
// the cell's lock, and returns the record as fn left it; missing is the
// answer when the shim records no such table.
func (s *Service) alter(c *kernel.Call, name string, missing *awserr.Error, fn func(t *Table, now clock.Instant) *awserr.Error) (Table, *awserr.Error) {
	return kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (Table, *awserr.Error) {
		t, ok := st.Tables[name]
		if !ok {
			return Table{}, missing
		}
		t = t.clone()
		if err := fn(&t, now); err != nil {
			return Table{}, err
		}
		st.Tables[name] = t
		return t.clone(), nil
	})
}

// answered is the handler of an operation the shims answer from their own
// state: after the checks a forwarded call gets and the licence gate, the
// output of fn is the answer, with the x-amz-crc32 header every DynamoDB
// answer carries. Local never sees the call.
func answered[In any](s *Service, fn func(ctx context.Context, c *kernel.Call, in *In) (smithy.Serializable, *awserr.Error)) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, _ kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		if err := preForward(c); err != nil {
			return kernel.RawResponse{}, err
		}
		if _, err := s.ready(ctx); err != nil {
			return kernel.RawResponse{}, err
		}
		out, err := fn(ctx, c, in)
		if err != nil {
			return kernel.RawResponse{}, err
		}
		resp, err := awsjson.Codec{Version: awsjson.V10}.Encode(c.Op(), string(c.ID()), out, http.StatusOK)
		if err != nil {
			return kernel.RawResponse{}, err
		}
		return rewrite(c, answer{}, kernel.RawResponse{Status: http.StatusOK, Header: resp.Header, Body: resp.Body})
	}
}

// invoker makes the TTL sweep's calls to Local through the pipeline, as
// children of the timer's call, so the journal, fault rules and access
// recorder see them like any call.
type invoker struct {
	s      *Service
	parent *kernel.Call
	target kernel.Target
}

func (x invoker) describe(ctx context.Context, table string, out *dynamodbapi.DescribeTableOutput) error {
	resp, err := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpDescribeTableRaw, x.target, &dynamodbapi.DescribeTableInput{TableName: table})
	return result(dynamodbapi.OpDescribeTableRaw.Descriptor(), resp, err, out)
}

// scan runs in page by page, following LastEvaluatedKey, and hands each
// item to each; an error from each ends the scan.
func (x invoker) scan(ctx context.Context, in *dynamodbapi.ScanInput, each func(item map[string]dynamodbapi.AttributeValue) error) error {
	for {
		var out dynamodbapi.ScanOutput
		resp, aerr := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpScanRaw, x.target, in)
		if err := result(dynamodbapi.OpScanRaw.Descriptor(), resp, aerr, &out); err != nil {
			return err
		}
		for _, item := range out.Items {
			if err := each(item); err != nil {
				return err
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		next := *in
		next.ExclusiveStartKey = out.LastEvaluatedKey
		in = &next
	}
}

// deleteExpired deletes the item key of table if its attribute still lies
// in the window, and reports whether it did: an item refreshed since the
// scan fails the condition and stays.
func (x invoker) deleteExpired(ctx context.Context, table, attribute string, key map[string]dynamodbapi.AttributeValue, expired window) (bool, error) {
	in := &dynamodbapi.DeleteItemInput{
		TableName: table, Key: key,
		ConditionExpression:       new(expiredCondition),
		ExpressionAttributeNames:  map[string]string{"#ttl": attribute},
		ExpressionAttributeValues: expired.values(),
	}
	resp, aerr := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpDeleteItemRaw, x.target, in)
	err := result(dynamodbapi.OpDeleteItemRaw.Descriptor(), resp, aerr, nil)
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ConditionalCheckFailedException") {
		return false, nil
	}
	return err == nil, err
}

// result reads the answer to a call the shims made into out (nil to
// ignore its body): understudy's refusal as its BoundaryError, Local's as a
// *LocalError, and a body that does not decode as an *AnswerError.
func result(op *registry.Operation, resp *kernel.RawOutput, aerr *awserr.Error, out smithy.Deserializable) error {
	if aerr != nil {
		return aerr.AsError()
	}
	return decodeAnswer(op, resp.Response, out)
}

// decodeAnswer reads one of Local's answers to op into out (nil to ignore
// its body); an error answer is a *LocalError.
func decodeAnswer(op *registry.Operation, resp kernel.RawResponse, out smithy.Deserializable) error {
	if resp.Status != http.StatusOK {
		return localError(op, resp)
	}
	if out == nil {
		return nil
	}
	r := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(resp.Body))}
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op, out); err != nil {
		return &AnswerError{Op: op.ID, Reason: err.Message}
	}
	return nil
}

// LocalError is DynamoDB Local refusing a call the shims made.
type LocalError struct {
	Op     ref.OperationID
	Status int
	// Type is the answer's __type, such as
	// com.amazonaws.dynamodb.v20120810#ResourceNotFoundException.
	Type    string
	Message string
}

func (e *LocalError) Error() string {
	return fmt.Sprintf("DynamoDB Local refused %s with %d %s: %s", e.Op, e.Status, e.Type, e.Message)
}

// is reports whether the error is the shape named name, in any namespace.
func (e *LocalError) is(name string) bool {
	_, short, _ := strings.Cut(e.Type, "#")
	return short == name
}

func localError(op *registry.Operation, resp kernel.RawResponse) *LocalError {
	e := &LocalError{Op: op.ID, Status: resp.Status, Message: string(resp.Body)}
	dec := jsonx.NewDecoder(resp.Body)
	doc, err := dec.ReadValue()
	if m, ok := doc.(document.Map); ok && err == nil {
		if t, ok := member(m, "__type").(document.String); ok {
			e.Type = string(t)
		}
		for _, key := range []string{"message", "Message"} {
			if text, ok := member(m, key).(document.String); ok {
				e.Message = string(text)
			}
		}
	}
	return e
}

// AnswerError is an answer from Local the shims cannot read.
type AnswerError struct {
	Op     ref.OperationID
	Reason string
}

func (e *AnswerError) Error() string {
	return fmt.Sprintf("DynamoDB Local's answer to %s cannot be read: %s", e.Op, e.Reason)
}

// Coverage notes of the operations the shims answer.
const (
	ttlNote = "aws-differs: understudy: an expired item is deleted on the emulator clock as soon as it expires, where AWS deletes within days; " +
		"the one change per hour AWS allows is not enforced"
	pitrNote = "point-in-time recovery is recorded and described; no backups are taken, and the restore operations are not implemented"
)

// bindShims binds the operations the shims answer without Local: tags,
// time to live and continuous backups.
func (s *Service) bindShims(b dynamodbapi.Binder) {
	b.TagResourceRaw(answered(s, s.tagResource), registry.Full{}, taggedTable(func(in *dynamodbapi.TagResourceInput) string { return in.ResourceArn }))
	b.UntagResourceRaw(answered(s, s.untagResource), registry.Full{}, taggedTable(func(in *dynamodbapi.UntagResourceInput) string { return in.ResourceArn }))
	b.ListTagsOfResourceRaw(answered(s, s.listTags), registry.Full{}, taggedTable(func(in *dynamodbapi.ListTagsOfResourceInput) string { return in.ResourceArn }))
	b.UpdateTimeToLiveRaw(answered(s, s.updateTimeToLive), registry.Partial{Note: ttlNote},
		tables(named(func(in *dynamodbapi.UpdateTimeToLiveInput) string { return in.TableName })))
	b.DescribeTimeToLiveRaw(answered(s, s.describeTimeToLive), registry.Partial{Note: ttlNote},
		tables(named(func(in *dynamodbapi.DescribeTimeToLiveInput) string { return in.TableName })))
	b.UpdateContinuousBackupsRaw(answered(s, s.updateContinuousBackups), registry.StoredOnly{Note: pitrNote},
		tables(named(func(in *dynamodbapi.UpdateContinuousBackupsInput) string { return in.TableName })))
	b.DescribeContinuousBackupsRaw(answered(s, s.describeContinuousBackups), registry.StoredOnly{Note: pitrNote},
		tables(named(func(in *dynamodbapi.DescribeContinuousBackupsInput) string { return in.TableName })))
}
