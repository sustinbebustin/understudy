package dynamodb_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/store"
)

// seedTables creates Locks, with tags, TTL and PITR, and Jobs, keyed by a
// hash and a range key, each holding enough items to span Scan pages.
func seedTables(h *harness) {
	h.Helper()
	h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Locks",`+
		`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST","Tags":[{"Key":"team","Value":"core"}]}`)
	h.mustCall("UpdateTimeToLive", `{"TableName":"Locks","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
	h.mustCall("UpdateContinuousBackups", `{"TableName":"Locks","PointInTimeRecoverySpecification":{"PointInTimeRecoveryEnabled":true,"RecoveryPeriodInDays":7}}`)
	h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"q","AttributeType":"S"},{"AttributeName":"n","AttributeType":"N"}],"TableName":"Jobs",`+
		`"KeySchema":[{"AttributeName":"q","KeyType":"HASH"},{"AttributeName":"n","KeyType":"RANGE"}],"ProvisionedThroughput":{"ReadCapacityUnits":5,"WriteCapacityUnits":5}}`)
	for i := range 5 {
		h.mustCall("PutItem", fmt.Sprintf(`{"TableName":"Locks","Item":{"id":{"S":"lock-%d"},"exp":{"N":"99999999999"},"owner":{"M":{"pid":{"N":"%d.50"},"tags":{"SS":["a","b"]}}}}}`, 4-i, i))
		h.mustCall("PutItem", fmt.Sprintf(`{"TableName":"Jobs","Item":{"q":{"S":"main"},"n":{"N":"%d"},"body":{"B":"AAEC"},"done":{"BOOL":false},"note":{"NULL":true}}}`, 10-i))
	}
	h.settle()
}

// TestLateExportResetImportRestoresEverything: items exported through
// Local, a reset that deletes every table in Local, then an import of the
// items and the cell sections bring back every item and everything the
// shim records, and the export of the result is the same bytes.
func TestLateExportResetImportRestoresEverything(t *testing.T) {
	runStore(t, func(h *harness, fake *tableStore) {
		seedTables(h)
		read := func() []string {
			return []string{
				h.call("ListTagsOfResource", `{"ResourceArn":"arn:aws:dynamodb:us-east-1:111111111111:table/Locks"}`).Body,
				h.call("DescribeTimeToLive", `{"TableName":"Locks"}`).Body,
				h.call("DescribeContinuousBackups", `{"TableName":"Locks"}`).Body,
				h.call("DescribeTable", `{"TableName":"Jobs"}`).Body,
			}
		}
		before := read()
		items, err := h.svc.ExportLate(h.Context())
		if err != nil {
			h.Fatal(err)
		}
		cells, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		paths := make([]string, 0, len(items))
		for _, s := range items {
			paths = append(paths, s.Path)
		}
		if want := []string{"111111111111/us-east-1/Jobs.ndjson", "111111111111/us-east-1/Locks.ndjson"}; !slices.Equal(paths, want) {
			h.Fatalf("item sections = %v, want %v", paths, want)
		}
		locks := strings.Split(strings.TrimSuffix(string(items[1].Data), "\n"), "\n")
		if len(locks) != 6 || !strings.HasPrefix(locks[0], `{"AttributeDefinitions":`) || !strings.Contains(locks[0], `"BillingMode":"PAY_PER_REQUEST"`) {
			h.Fatalf("Locks section = %q, want the CreateTable request and 5 items", locks)
		}
		for i, line := range locks[1:] {
			if want := fmt.Sprintf(`{"exp":{"N":"99999999999"},"id":{"S":"lock-%d"},"owner":{"M":{"pid":{"N":"%d.50"},"tags":{"SS":["a","b"]}}}}`, i, 4-i); line != want {
				h.Errorf("Locks line %d = %s, want %s", i+2, line, want)
			}
		}

		if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if names := fake.tableNames(); len(names) != 0 {
			h.Fatalf("Local holds %v after the reset", names)
		}

		if err := h.svc.ImportLate(h.Context(), items); err != nil {
			h.Fatal(err)
		}
		if err := h.svc.Import(cells); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settle()
		if after := read(); !slices.Equal(after, before) {
			h.Errorf("reads after the import differ:\n%v\nwant\n%v", after, before)
		}
		again, err := h.svc.ExportLate(h.Context())
		if err != nil {
			h.Fatal(err)
		}
		for i := range items {
			if !bytes.Equal(again[i].Data, items[i].Data) {
				h.Errorf("export after the import of %s =\n%s\nwant\n%s", items[i].Path, again[i].Data, items[i].Data)
			}
		}
		if n := len(fake.items("Jobs")); n != 5 {
			h.Errorf("Jobs holds %d items after the import, want 5", n)
		}
	})
}

// TestUnrecordedTablesSurviveSaveAndLoad: a table Local holds that the
// shim does not record, such as one kept under --persist, is exported with
// its items, so the load that deletes every table of the scope brings it
// back, and the first shim operation adopts it.
func TestUnrecordedTablesSurviveSaveAndLoad(t *testing.T) {
	runStore(t, func(h *harness, fake *tableStore) {
		seedTables(h)
		fake.serve("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Kept",`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		fake.serve("PutItem", `{"TableName":"Kept","Item":{"id":{"S":"a"},"n":{"N":"1"}}}`)
		items, err := h.svc.ExportLate(h.Context())
		if err != nil {
			h.Fatal(err)
		}
		cells, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.ImportLate(h.Context(), items); err != nil {
			h.Fatal(err)
		}
		if err := h.svc.Import(cells); err != nil {
			h.Fatal(err)
		}
		if names := fake.tableNames(); !slices.Equal(names, []string{"Jobs", "Kept", "Locks"}) {
			h.Fatalf("Local holds %v after the load, want the unrecorded table Kept back too", names)
		}
		if got := fake.items("Kept"); len(got) != 1 || string(got[`{"id":{"S":"a"}}`]["n"]) != `{"N":"1"}` {
			h.Errorf("Kept holds %v after the load, want its one item", got)
		}
		if got := h.mustCall("DescribeTimeToLive", `{"TableName":"Kept"}`); path(got, "TimeToLiveDescription", "TimeToLiveStatus") != "DISABLED" {
			h.Errorf("DescribeTimeToLive of the reloaded table = %v, want it adopted with TTL disabled", got)
		}
	})
}

// TestImportLateRefusesUnreadableSectionsFirst: a section of another
// schema version, a path that names no table, an item that is not one,
// a first line that is no valid CreateTable request, or one creating
// another table than the path names is refused before any table in Local
// is deleted.
func TestImportLateRefusesUnreadableSectionsFirst(t *testing.T) {
	runStore(t, func(h *harness, fake *tableStore) {
		seedTables(h)
		good, err := h.svc.ExportLate(h.Context())
		if err != nil {
			h.Fatal(err)
		}
		for name, bad := range map[string]store.Section{
			"version": {Path: good[0].Path, Version: good[0].Version + 1, Data: good[0].Data},
			"path":    {Path: "111111111111/Jobs.ndjson", Version: good[0].Version, Data: good[0].Data},
			"suffix":  {Path: "111111111111/us-east-1/Jobs.json", Version: good[0].Version, Data: good[0].Data},
			"item":    {Path: good[0].Path, Version: good[0].Version, Data: append(bytes.Clone(good[0].Data), "[1]\n"...)},
			"empty":   {Path: good[0].Path, Version: good[0].Version},
			"create":  {Path: good[0].Path, Version: good[0].Version, Data: []byte("{\"TableName\":\"Jobs\"}\n")},
			"json":    {Path: good[0].Path, Version: good[0].Version, Data: []byte("CreateTable Jobs\n")},
			"other":   {Path: good[0].Path, Version: good[0].Version, Data: good[1].Data},
		} {
			err := h.svc.ImportLate(h.Context(), []store.Section{bad})
			if err == nil {
				h.Errorf("%s: ImportLate accepted %+v", name, bad)
			}
			var mismatch *store.VersionMismatchError
			if name == "version" && !errors.As(err, &mismatch) {
				h.Errorf("version: ImportLate = %v, want a *store.VersionMismatchError", err)
			}
			var section *dynamodb.SectionError
			if name != "version" && !errors.As(err, &section) {
				h.Errorf("%s: ImportLate = %v, want a *dynamodb.SectionError", name, err)
			}
		}
		if names := fake.tableNames(); !slices.Equal(names, []string{"Jobs", "Locks"}) {
			h.Errorf("Local holds %v after refused imports, want both tables kept", names)
		}
	})
}

// TestResetWithoutTablesNeverStartsLocal: a reset of a scope the shim
// recorded no table in sends Local nothing, so a server whose DynamoDB
// licence is not accepted resets cleanly.
func TestResetWithoutTablesNeverStartsLocal(t *testing.T) {
	runWith(t, nil, func(h *harness) {
		if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
			h.Errorf("ResetLate = %v", err)
		}
		if ss, err := h.svc.ExportLate(h.Context()); err != nil || len(ss) != 0 {
			h.Errorf("ExportLate = %v, %v; want nothing", ss, err)
		}
		if err := h.svc.ImportLate(h.Context(), nil); err != nil {
			h.Errorf("ImportLate of nothing = %v", err)
		}
		if n, err := h.svc.SweepTTL(h.Context()); n != 0 || err != nil {
			h.Errorf("SweepTTL = %d, %v", n, err)
		}
	})
}
