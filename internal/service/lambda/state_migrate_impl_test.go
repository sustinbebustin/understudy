package lambda

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
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

// A mapping that disabled itself because its stream closed keeps that
// reason across a save and load, and a section of version 7 or earlier,
// written before the field existed, loads its mappings as not closed.
func TestStreamClosedRoundTripsAndOlderSectionsLoad(t *testing.T) {
	st := newState(kindScope(t))
	st.Mappings["s"] = Mapping{UUID: "s", Source: streamSource, SourceKind: esm.DynamoDBStream{Position: esm.TrimHorizon{}}, Function: "f", BatchSize: 100, StreamClosed: true}
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	if m := got.Mappings["s"]; !m.StreamClosed || m.Enabled {
		t.Errorf("after the round trip: StreamClosed %t, Enabled %t; want true, false", m.StreamClosed, m.Enabled)
	}
	older := `{"mappings":{"s":{"uuid":"s","source":"` + streamSource + `",` +
		`"sourceKind":{"kind":"dynamodb-stream","position":{"kind":"trim-horizon"},"settings":{"recordAge":{"kind":"any-age"},"retries":{"kind":"unlimited"},"onFailure":{"kind":"none"}}},` +
		`"function":"f","batchSize":100,"window":0,"enabled":false}}}`
	loaded := newState(kindScope(t))
	if err := stateCodec().Unmarshal([]byte(older), &loaded); err != nil {
		t.Fatalf("Unmarshal %s: %v", older, err)
	}
	if m, ok := loaded.Mappings["s"]; !ok || m.StreamClosed {
		t.Errorf("older section: mapping %+v, want it loaded and not StreamClosed", m)
	}
}

// streamSource is a DynamoDB stream's ARN.
const streamSource = "arn:aws:dynamodb:us-east-1:000000000000:table/Orders/stream/2000-01-01T00:00:00.000"

// A deleted function's highest version number is saved with the state, and
// a function created under its name takes it over.
func TestRetiredVersionsRoundTrip(t *testing.T) {
	st := kindState(t)
	st.Functions["fn"] = Function{Name: "fn", LastVersion: 3}
	f := st.Functions["fn"]
	st.retireFunction(&f)
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	if n := got.reclaimVersions("fn"); n != 3 {
		t.Errorf("LastVersion of fn created after the round trip = %d, want 3", n)
	}
	if n := got.reclaimVersions("fn"); n != 0 {
		t.Errorf("LastVersion of fn created a second time = %d, want 0: the first took the number over", n)
	}
}

// A version 4 section, which kept no deleted function's number and no
// layer policy history or sizes, loads with no numbers remembered, its
// layer versions as never having had a policy and its statements measured
// at their JSON's length; a live function keeps its own number.
func TestVersion4SectionLoadsWithNoRetiredVersions(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	section := `{"functions":{"live":{"name":"live","created":"2000-01-01T00:00:00Z","latest":{"number":"$LATEST"},"lastVersion":3,"generation":1}},` +
		`"layers":{"l":{"name":"l","lastVersion":1,"versions":{"1":{"number":1,"description":"","sha256":"","size":0,"unzipped":0,"blob":"` + digest + `",` +
		`"policy":[{"sid":"s","json":"{\"Sid\":\"s\"}"}],"policyRevision":"r"}}}},"serial":7}`
	st := newState(kindScope(t))
	if err := stateCodec().Unmarshal([]byte(section), &st); err != nil {
		t.Fatalf("Unmarshal %s: %v", section, err)
	}
	if st.RetiredVersions != nil || st.reclaimVersions("gone") != 0 {
		t.Errorf("RetiredVersions = %v, want none", st.RetiredVersions)
	}
	if n := st.Functions["live"].LastVersion; n != 3 {
		t.Errorf("live LastVersion = %d, want 3", n)
	}
	if v := st.Layers["l"].Versions[1]; v.HadPolicy || v.Policy[0].measuredSize() != len(`{"Sid":"s"}`) {
		t.Errorf("version 4 layer version HadPolicy = %t, measured size %d; want false and %d", v.HadPolicy, v.Policy[0].measuredSize(), len(`{"Sid":"s"}`))
	}
}

// A layer version's having had a policy and its statements' measured sizes
// are saved with the state; a version 5 section, without either, reads as
// never having had a policy, its statements measured at their JSON's
// length, and keeps its deleted functions' numbers.
func TestLayerPolicyFieldsRoundTripAndDefaultInAVersion5Section(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	archive, err := blob.ParseRef(digest)
	if err != nil {
		t.Fatal(err)
	}
	st := newState(kindScope(t))
	st.Layers = map[string]Layer{"l": {Name: "l", LastVersion: 2, Versions: map[int64]LayerVersion{
		1: {Number: 1, Blob: archive, HadPolicy: true},
		2: {Number: 2, Blob: archive, Policy: []Statement{{Sid: "s", JSON: `{"Sid":"s"}`, MeasuredSize: 4}}, PolicyRevision: "r", HadPolicy: true},
	}}}
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	v1, v2 := got.Layers["l"].Versions[1], got.Layers["l"].Versions[2]
	if !v1.HadPolicy || !v2.HadPolicy || v2.Policy[0].measuredSize() != 4 {
		t.Errorf("after a round trip HadPolicy = %t, %t and measured size %d; want true, true and 4", v1.HadPolicy, v2.HadPolicy, v2.Policy[0].measuredSize())
	}

	version5 := `{"layers":{"l":{"name":"l","lastVersion":1,"versions":{"1":{"number":1,"description":"","sha256":"","size":0,"unzipped":0,"blob":"` + digest + `",` +
		`"policy":[{"sid":"s","json":"{\"Sid\":\"s\"}"}],"policyRevision":"r"}}}},"retiredVersions":{"gone":3}}`
	old := newState(kindScope(t))
	if err := stateCodec().Unmarshal([]byte(version5), &old); err != nil {
		t.Fatalf("Unmarshal a version 5 section: %v", err)
	}
	v := old.Layers["l"].Versions[1]
	if v.HadPolicy || v.Policy[0].measuredSize() != len(`{"Sid":"s"}`) {
		t.Errorf("version 5 layer version HadPolicy = %t, measured size %d; want false and %d", v.HadPolicy, v.Policy[0].measuredSize(), len(`{"Sid":"s"}`))
	}
	if n := old.reclaimVersions("gone"); n != 3 {
		t.Errorf("LastVersion of gone created after loading a version 5 section = %d, want 3", n)
	}
}

// Directory code is saved with the state, on $LATEST and on a published
// version alike.
func TestDirCodeRoundTrip(t *testing.T) {
	st := newState(kindScope(t))
	code := DirCode{Dir: "/srv/builds/fn"}
	st.Functions["fn"] = Function{Name: "fn", Latest: Version{Number: latest, Code: code}, Versions: []Version{{Number: "1", Code: code}}, LastVersion: 1}
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	f := got.Functions["fn"]
	if f.Latest.Code != code || len(f.Versions) != 1 || f.Versions[0].Code != code {
		t.Errorf("after a round trip $LATEST code = %#v, versions %+v; want %#v on both", f.Latest.Code, f.Versions, code)
	}
}

// A version 8 section, written before directory code existed, loads as is.
func TestVersion8SectionLoads(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := New(k.Deps(lambdaapi.Service()), Options{})
		section := store.Section{
			Path: "000000000000/us-east-1.json", Version: 8,
			Data: []byte(`{"functions":{"fn":{"name":"fn","created":"2000-01-01T00:00:00Z","latest":{"number":"$LATEST"},"lastVersion":2,"generation":1}},"serial":3}`),
		}
		if err := svc.Import([]store.Section{section}); err != nil {
			t.Fatalf("Import of a version 8 section: %v", err)
		}
		cell, ok := svc.cells.Lookup(kindScope(t))
		if !ok {
			t.Fatal("no state for the section's scope after the import")
		}
		if n := store.Read(cell, func(st *State) int { return st.Functions["fn"].LastVersion }); n != 2 {
			t.Errorf("fn LastVersion after loading a version 8 section = %d, want 2", n)
		}
	}, kerneltest.Services(lambdaapi.Service()))
}

// An image code keeps the kind of its digest across a save and load, so
// invoke resolves the same single reference after it.
func TestImageDigestKindRoundTrip(t *testing.T) {
	st := newState(kindScope(t))
	codes := map[string]ImageCode{
		"built":  {URI: "local/fn:latest", Digest: "sha256:" + strings.Repeat("4c", 32), DigestKind: DigestImageID},
		"pulled": {URI: "registry.example/fn:1", Digest: "sha256:" + strings.Repeat("e1", 32), DigestKind: DigestRepository},
	}
	for name, code := range codes {
		st.Functions[name] = Function{Name: name, Latest: Version{Number: latest, Code: code, Status: Active{Update: Successful{}}}}
	}
	data, err := stateCodec().Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newState(kindScope(t))
	if err := stateCodec().Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal %s: %v", data, err)
	}
	for name, want := range codes {
		if code := got.Functions[name].Latest.Code; code != want {
			t.Errorf("%s code after the round trip = %#v, want %#v", name, code, want)
		}
	}
}

// A version 6 section, whose image codes carry no digest kind, loads with
// the kind unrecorded, which invoke resolves by trying both forms.
func TestVersion6SectionLoadsImageCodeWithoutDigestKind(t *testing.T) {
	const section = `{"functions":{"img":{"name":"img","created":"2000-01-01T00:00:00Z",` +
		`"latest":{"number":"$LATEST","code":{"kind":"image","uri":"local/fn:latest","digest":"sha256:4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c"},` +
		`"status":{"kind":"active","update":{"kind":"successful"}}},"generation":1}},"serial":1}`
	st := newState(kindScope(t))
	if err := stateCodec().Unmarshal([]byte(section), &st); err != nil {
		t.Fatalf("Unmarshal %s: %v", section, err)
	}
	code, ok := st.Functions["img"].Latest.Code.(ImageCode)
	if !ok || code.Digest == "" || code.DigestKind != DigestUnrecorded {
		t.Errorf("img code = %#v, want image code with its digest and no kind", st.Functions["img"].Latest.Code)
	}
}

// A section whose image code names a kind understudy never writes is
// refused rather than loaded with a reference invoke cannot resolve.
func TestImageDigestKindRefusesAnUnknownKind(t *testing.T) {
	const section = `{"functions":{"img":{"name":"img","created":"2000-01-01T00:00:00Z",` +
		`"latest":{"number":"$LATEST","code":{"kind":"image","uri":"local/fn:latest","digest":"sha256:4c","digestKind":"tag"},` +
		`"status":{"kind":"active","update":{"kind":"successful"}}},"generation":1}},"serial":1}`
	st := newState(kindScope(t))
	if err := stateCodec().Unmarshal([]byte(section), &st); err == nil {
		t.Errorf("Unmarshal of digestKind %q = nil error, want it refused", "tag")
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
