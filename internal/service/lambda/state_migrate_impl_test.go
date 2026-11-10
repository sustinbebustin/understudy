package lambda

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// unkindedSection is a version 3 section: an SQS mapping and a stream
// mapping, each with its scaling beside its source kind.
const unkindedSection = `{"mappings":{` +
	`"q":{"uuid":"q","source":"arn:aws:sqs:us-east-1:000000000000:orders","sourceKind":{"kind":"sqs"},"function":"f","batchSize":10,"window":0,` +
	`"scaling":{"kind":"maximum-concurrency","n":3},"enabled":true},` +
	`"s":{"uuid":"s","source":"arn:aws:dynamodb:us-east-1:000000000000:table/Orders/stream/2000-01-01T00:00:00.000",` +
	`"sourceKind":{"kind":"dynamodb-stream","position":{"kind":"trim-horizon"},"settings":{"recordAge":{"kind":"any-age"},"retries":{"kind":"unlimited"},"onFailure":{"kind":"none"}}},` +
	`"function":"f","batchSize":100,"window":0,"scaling":{"kind":"unbounded"},"enabled":true}}}`

// A version 3 section loads as version 4: the SQS mapping's scaling moves
// into its source kind and the stream mapping's is dropped
// (lambda-stream-sources.md 3.4).
func TestMigrateUnkindedScalingMovesScalingIntoTheSQSKind(t *testing.T) {
	data, err := migrateUnkindedScaling([]byte(unkindedSection))
	if err != nil {
		t.Fatalf("migrateUnkindedScaling: %v", err)
	}
	st := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &st); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	q, ok := st.Mappings["q"].SourceKind.(esm.SQS)
	if !ok || q.Scaling != (esm.MaximumConcurrency{N: 3}) {
		t.Errorf("SQS mapping kind = %#v, want esm.SQS with MaximumConcurrency 3", st.Mappings["q"].SourceKind)
	}
	s, ok := st.Mappings["s"].SourceKind.(esm.DynamoDBStream)
	if !ok || s.Position != (esm.TrimHorizon{}) || s.Settings.OnFailure != (esm.NoDestination{}) {
		t.Errorf("stream mapping kind = %#v, want the TRIM_HORIZON stream kind", st.Mappings["s"].SourceKind)
	}
}

// A malformed version 3 section is refused rather than loaded half
// migrated.
func TestMigrateUnkindedScalingRefusesMalformedData(t *testing.T) {
	if _, err := migrateUnkindedScaling([]byte(`{"mappings":{"q":{"sourceKind":7}}}`)); err == nil {
		t.Error("migrateUnkindedScaling of a mapping whose sourceKind is not an object = nil error")
	}
}

// Checkpoints are saved with the state and dropped with their mapping.
func TestCheckpointsRoundTripAndGoWithTheirMapping(t *testing.T) {
	st := kindState(t)
	st.setCheckpoint("u1", "shard-a", shardlog.After{Seq: "100000000000000000001"})
	st.setCheckpoint("u1", "shard-a", shardlog.After{Seq: "100000000000000000002"})
	st.setCheckpoint("u1", "shard-b", shardlog.TrimHorizon{})
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	if p := got.checkpoint("u1", "shard-a"); p != (shardlog.After{Seq: "100000000000000000002"}) {
		t.Errorf("shard-a checkpoint = %#v, want After 100000000000000000002", p)
	}
	if p := got.checkpoint("u1", "shard-b"); p != (shardlog.TrimHorizon{}) {
		t.Errorf("shard-b checkpoint = %#v, want TrimHorizon", p)
	}
	got.dropMapping("u1")
	if _, ok := got.Mappings["u1"]; ok || got.Checkpoints["u1"] != nil {
		t.Errorf("after dropMapping: mapping kept %t, checkpoints %v", ok, got.Checkpoints["u1"])
	}
}
