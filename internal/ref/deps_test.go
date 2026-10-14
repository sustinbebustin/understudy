package ref_test

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// importRule lists what one package, or every package matching a pattern
// ending in "/...", may import from the repository. On both sides "/..."
// matches the named package itself and every package below it, and "{self}"
// stands for the importer's own service directory under internal/service.
type importRule struct {
	pkg     string
	imports []string
}

// importRules mirrors ARCHITECTURE.md section 2. An edge section 2 does not
// list, and every row for a package section 2 does not name, carries a comment
// giving its reason. Test files are exempt: cross-service imports are allowed
// in tests.
func importRules() []importRule {
	codec := []string{"internal/protocol/smithy/...", "internal/awserr", "internal/ref", "internal/registry", "internal/aws"}
	withCodec := func(extra ...string) []string { return append(slices.Clone(codec), extra...) }
	kernel := []string{
		"internal/registry", "internal/journal", "internal/fault", "internal/access", "internal/identity",
		"internal/clock", "internal/ids", "internal/store", "internal/delivery", "internal/awserr", "internal/ref",
		// Call carries an aws.Scope (ResolveScope), and Validate selects
		// per-member identities from the generated smithy descriptors.
		"internal/aws", "internal/protocol/smithy/...",
		// ServiceDeps carries the blob store and the Docker client (ARCHITECTURE.md 3.2).
		"internal/blob", "internal/docker",
		// Call carries the credentials the edge verified (VerifiedAuth),
		// which S3 reads to verify aws-chunked chunk signatures
		// (ARCHITECTURE.md 7).
		"internal/auth",
	}
	service := []string{
		"internal/service/{self}/...", "internal/api/...", "internal/kernel", "internal/store", "internal/clock",
		"internal/ids", "internal/blob", "internal/delivery", "internal/fidelity", "internal/evidence",
		// The vocabulary the generated ops, errors and descriptors expose.
		"internal/aws", "internal/ref", "internal/awserr", "internal/registry", "internal/protocol/smithy/...",
		// "The engines it needs" (ARCHITECTURE.md 2).
		"internal/checksum", "internal/awschunked", "internal/ruler", "internal/logsfilter", "internal/schedexpr",
		"internal/policydoc", "internal/docker", "internal/topology",
		// Access keys and execution-role credentials are minted through
		// identity.Store; the DynamoDB proxy rewrites the SigV4 credential
		// scope through auth.
		"internal/identity", "internal/auth",
		// Persisted sums use the jsonx tagged-union convention (ARCHITECTURE.md
		// 13); the DynamoDB proxy rewrites DynamoDB Local responses through
		// jsonx.Value.
		"internal/protocol/jsonx",
		// A handler describes the events it causes with journal
		// Detail values and records them through kernel.Call.Record or
		// kernel.Tx.Record, which stamp the cause (ARCHITECTURE.md 10).
		"internal/journal",
		// Keyset pagination for listings. Its client-side checker,
		// internal/paging/pagingtest, is for test files, which are exempt.
		"internal/paging",
	}
	return []importRule{
		// Leaves.
		{"internal/aws", nil},
		{"internal/evidence", nil},
		{"internal/clock", nil},
		{"internal/blob", nil},
		{"internal/checksum", nil},
		{"internal/awschunked", nil},
		{"internal/protocol/xmlw", nil},
		{"internal/protocol/cbor", nil},
		{"internal/protocol/form", nil},
		{"internal/schedexpr", nil},
		{"internal/policydoc", nil},
		{"internal/docker", nil},
		// The Smithy document type, below protocol/smithy.
		{"internal/protocol/smithy/document", nil},
		{"internal/ids", nil},
		{"internal/ruler", nil},
		{"internal/logsfilter", nil},
		{"internal/paging", nil},
		{"internal/paging/pagingtest", nil},
		{"internal/ref", []string{"internal/aws"}},
		{"internal/store", []string{"internal/aws"}}, // key codecs for aws.Scope and aws.AccountID
		{"internal/fidelity", []string{"internal/evidence"}},
		{"internal/topology", []string{"internal/docker"}},
		{"internal/docker/dockertest", []string{"internal/docker"}},
		{"internal/delivery", []string{"internal/aws", "internal/ref"}},
		{"internal/protocol/smithy", []string{"internal/protocol/smithy/document"}},

		// Vocabulary and platform.
		{"internal/awserr", []string{"internal/ref", "internal/evidence", "internal/protocol/smithy/..."}},
		{"internal/registry", []string{"internal/awserr", "internal/protocol/smithy/...", "internal/ref", "internal/aws"}},
		// ADR 0007 amendment and ARCHITECTURE.md 10: Delivery events carry a delivery.Mechanism.
		{"internal/journal", []string{"internal/ref", "internal/aws", "internal/clock", "internal/ids", "internal/delivery"}},
		// Rules export as JSON through the jsonx tagged-union convention.
		{"internal/fault", []string{"internal/ref", "internal/aws", "internal/awserr", "internal/protocol/smithy/...", "internal/ids", "internal/delivery", "internal/protocol/jsonx"}},
		// Recorder entries carry registry.IAMAction.
		{"internal/access", []string{"internal/ref", "internal/aws", "internal/policydoc", "internal/registry"}},
		{"internal/identity", []string{"internal/aws", "internal/ids", "internal/clock"}},
		{"internal/auth", []string{"internal/aws", "internal/awserr", "internal/clock", "internal/ref"}},
		{"internal/kernel", kernel},
		{"internal/kernel/kerneltest", append(slices.Clone(kernel), "internal/kernel")},
		{"internal/outbox", []string{
			"internal/kernel", "internal/delivery", "internal/journal", "internal/store", "internal/clock",
			"internal/api/...", // attempts call the generated SendMessage, Invoke and Publish ops
			// Entries live in Partitioned[aws.Scope, State], Enqueue takes a
			// ref.Cause and returns an ids.DeliveryID, and Stateful exports
			// through jsonx (ARCHITECTURE.md 13).
			"internal/aws", "internal/ref", "internal/ids", "internal/protocol/jsonx",
			// Phase Delivery fault evaluation; client-side failures
			// (QueueDoesNotExist) are told apart by their awserr identity.
			"internal/fault", "internal/awserr",
			// Access advisories call access.Advise with the target's stored
			// policydoc.Document and the registry.IAMAction of the attempt.
			"internal/access", "internal/policydoc", "internal/registry",
		}},
		// The manifest records JournalNext as a ref.EventID.
		{"internal/snapshot", []string{"internal/store", "internal/blob", "internal/protocol/jsonx", "internal/clock", "internal/ids", "internal/ref"}},
		// The report keys suites and services by ref.ProtocolID and
		// ref.ServiceID, writes its sums in the jsonx tagged-union
		// convention and reads overlay files with the jsonx decoder.
		{"internal/coverage", []string{"internal/kernel", "internal/registry", "internal/fidelity", "internal/evidence", "internal/protocoltest", "internal/ref", "internal/protocol/jsonx"}},

		// Wire protocols: never a service or a generated package. A codec
		// claims through the registry and reads the descriptor's identities.
		{"internal/protocol/jsonx", []string{"internal/protocol/smithy/...", "internal/awserr", "internal/ref"}},
		{"internal/protocol/restbind", codec},
		{"internal/protocol/awsjson", withCodec("internal/protocol/jsonx")},
		{"internal/protocol/awsquery", withCodec("internal/protocol/form", "internal/protocol/xmlw")},
		{"internal/protocol/restjson", withCodec("internal/protocol/restbind", "internal/protocol/jsonx")},
		{"internal/protocol/restxml", withCodec("internal/protocol/restbind", "internal/protocol/xmlw")},
		{"internal/protocol/rpcv2cbor", withCodec("internal/protocol/cbor")},

		// Generated API packages and services.
		{"internal/api/...", []string{"internal/protocol/smithy/...", "internal/awserr", "internal/registry", "internal/kernel", "internal/aws", "internal/ref"}},
		{"internal/service/...", service},
		// A Synthesized call is encoded for DynamoDB Local through the
		// awsJson codec, the bytes a client would send (ARCHITECTURE.md 15).
		{"internal/service/dynamodb", append(slices.Clone(service), "internal/protocol/awsjson")},
		// The raw S3 Control tag operations read and write their XML
		// bodies with xmlw and stamp x-amz-id-2 as the restXml codec does.
		{"internal/service/s3", append(slices.Clone(service), "internal/protocol/xmlw", "internal/protocol/restxml")},

		// Front door, composition root and tools.
		{"internal/edge", []string{
			"internal/kernel", "internal/registry", "internal/auth", "internal/identity",
			// Codecs are keyed by ref.ProtocolID and the Codec interface
			// decodes into smithy descriptors; identity resolution yields an
			// aws.Scope.
			"internal/ref", "internal/protocol/smithy/...", "internal/aws",
			// The Unclaimed journal event and its AWS-shaped 400, request ids
			// and the wall-time Date header from Clock.Wall.
			"internal/journal", "internal/awserr", "internal/ids", "internal/clock",
			// The admin API's JSON error envelope for LocalStack paths
			// (ARCHITECTURE.md 4.1 rule 1); admin imports nothing in the
			// repository.
			"admin",
		}},
		{"internal/server", []string{"internal/...", "admin"}},
		{"internal/server/admin", []string{
			"admin", "internal/kernel", "internal/journal", "internal/snapshot", "internal/fault", "internal/clock",
			"internal/access", "internal/registry", "internal/coverage", "internal/aws", "internal/ref", "internal/awserr",
			"internal/ids", "internal/store", "internal/blob", "internal/policydoc", "internal/identity", "internal/protocol/jsonx",
		}},
		{"internal/config", []string{"internal/aws", "internal/ref", "internal/clock", "internal/fault", "internal/protocol/jsonx"}},
		// Docker-tagged tests reach the daemon serve would use, resolved by
		// config; only test files import it.
		{"internal/config/configtest", []string{"internal/config", "internal/docker"}},
		// Serve maps config.Config onto server.Config; coverage runs
		// offline in process and renders, filters and adds overlays through
		// coverage; snapshot inspect and diff read local archives; doctor and
		// cleanup reach Docker and detect the topology; env checks --region
		// against the region names in the aws leaf. None is a service.
		{"internal/cli", []string{
			"admin", "internal/config", "internal/server",
			"internal/coverage", "internal/snapshot", "internal/docker", "internal/topology", "internal/aws",
		}},
		{"admin", nil},
		{"understudytest", []string{"admin", "internal/config", "internal/server"}},
		{"cmd/understudy", []string{"internal/cli", "internal/server", "internal/config", "admin"}},
		{"internal/codegen", []string{"internal/evidence", "internal/protocol/smithy/...", "internal/ref", "internal/registry", "internal/awserr", "internal/aws"}},
		// The -service flag is a ref.ServiceID, a leaf codegen imports too.
		{"internal/cmd/gen", []string{"internal/codegen", "internal/ref"}},
		{"internal/cmd/vendor", nil},
		// The golden fixture is generated like internal/api/..., so it
		// mirrors that row; serdetest holds the serde test doubles, and
		// validationpolicy is the policy runtime codegen embeds and copies.
		{"internal/codegen/internal/goldenapi", []string{"internal/protocol/smithy/...", "internal/awserr", "internal/registry", "internal/kernel", "internal/aws", "internal/ref"}},
		{"internal/codegen/serdetest", []string{"internal/protocol/smithy/..."}},
		{"internal/codegen/validationpolicy", []string{"internal/awserr"}},
		// The journal ring rows read journal.DefaultCapacity and
		// DefaultMaxBytes rather than copying them.
		{"internal/cmd/budgets", []string{"internal/journal"}},
		{"internal/protocoltest", []string{
			"internal/protocoltest/gen/...", "internal/protocol/...", "internal/registry", "internal/awserr", "internal/ref",
			"internal/aws", "internal/evidence", "internal/kernel", "internal/edge",
		}},
		// Each generated corpus service's cases_gen.go builds its tables
		// from the case vocabulary in internal/protocoltest/gen (case.go).
		{"internal/protocoltest/gen/...", []string{"internal/protocoltest/gen", "internal/protocol/smithy/...", "internal/awserr", "internal/registry", "internal/kernel", "internal/aws", "internal/ref"}},
		{"internal/smoke", nil},
		{"internal/parity", []string{"internal/snapshot", "internal/evidence", "internal/protocol/jsonx"}},
		// Docker-tagged checks of the release image; no runtime code.
		{"internal/distribution", nil},
	}
}

func TestImportDirection(t *testing.T) {
	root, module := moduleRoot(t)
	violations, err := importViolations(root, module, importRules())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func TestImportDirectionReportsSyntheticViolations(t *testing.T) {
	root := t.TempDir()
	const module = "example.com/m"
	files := map[string]string{
		// A leaf importing the kernel.
		"internal/aws/bad.go": `package aws

import _ "example.com/m/internal/kernel"
`,
		// Test files are exempt.
		"internal/aws/bad_test.go": `package aws

import _ "example.com/m/internal/kernel"
`,
		// An allowed edge.
		"internal/kernel/kernel.go": `package kernel

import (
	"fmt"

	_ "example.com/m/internal/registry"
)

var _ = fmt.Sprint
`,
		// A service may import its own subpackage, never another service.
		"internal/service/sqs/sqs.go": `package sqs

import (
	_ "example.com/m/internal/service/sns"
	_ "example.com/m/internal/service/sqs/attrs"
)
`,
		// The base of a "/..." row follows that row.
		"internal/api/generate.go": `package api

import (
	_ "example.com/m/internal/registry"
	_ "example.com/m/internal/journal"
)
`,
		// A package with no row.
		"internal/newpkg/newpkg.go": `package newpkg
`,
		// testdata is not a package.
		"internal/aws/testdata/x.go": `package x

import _ "example.com/m/internal/kernel"
`,
		// A "//go:build ignore" helper is never built into its directory's package.
		"internal/aws/gen.go": `//go:build ignore

package main

import _ "example.com/m/internal/kernel"
`,
		// Any other constraint still builds into the package.
		"internal/aws/linux.go": `//go:build linux

package aws

import _ "example.com/m/internal/journal"
`,
	}
	for name, src := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := importViolations(root, module, importRules())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/api imports internal/journal, which ARCHITECTURE.md section 2 does not allow; it may import internal/protocol/smithy/..., internal/awserr, internal/registry, internal/kernel, internal/aws, internal/ref",
		"internal/aws imports internal/journal, which ARCHITECTURE.md section 2 does not allow; it may import nothing in the repository",
		"internal/aws imports internal/kernel, which ARCHITECTURE.md section 2 does not allow; it may import nothing in the repository",
		"internal/newpkg has no row in the import table of internal/ref/deps_test.go; add one that mirrors ARCHITECTURE.md section 2",
		"internal/service/sqs imports internal/service/sns, which ARCHITECTURE.md section 2 does not allow; a service reaches another through kernel.Invoke with the generated Op",
	}
	if !slices.Equal(got, want) {
		t.Errorf("importViolations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// importViolations parses the import specs of every non-test Go file in the
// packages under root's internal, admin, understudytest and cmd trees and
// returns one message per disallowed edge, sorted. Packages are paths
// relative to root; directories named testdata or starting with . or _ are
// skipped, as the go tool does.
func importViolations(root, module string, rules []importRule) ([]string, error) {
	imports := map[string]map[string]bool{}
	for _, top := range []string{"internal", "admin", "understudytest", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			switch {
			case os.IsNotExist(err):
				return filepath.SkipDir
			case err != nil:
				return err
			case d.IsDir():
				if name := d.Name(); name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
					return filepath.SkipDir
				}
				return nil
			case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			pkg := filepath.ToSlash(rel)
			if imports[pkg] == nil {
				imports[pkg] = map[string]bool{}
			}
			return addImports(imports[pkg], path, module)
		})
		if err != nil {
			return nil, err
		}
	}
	var violations []string
	for pkg, deps := range imports {
		rule, ok := ruleFor(rules, pkg)
		if !ok {
			violations = append(violations, pkg+" has no row in the import table of internal/ref/deps_test.go; add one that mirrors ARCHITECTURE.md section 2")
			continue
		}
		for dep := range deps {
			if !allowed(rule, pkg, dep) {
				violations = append(violations, fmt.Sprintf("%s imports %s, which ARCHITECTURE.md section 2 does not allow; %s", pkg, dep, hint(rule, pkg, dep)))
			}
		}
	}
	slices.Sort(violations)
	return violations, nil
}

// addImports records the repository imports of one file, relative to the
// module path. A file constrained by "//go:build ignore" is skipped: the go
// tool never builds it into the package (generator helpers use it).
func addImports(into map[string]bool, path, module string) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return err
	}
	if ignored(f) {
		return nil
	}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return err
		}
		if dep, ok := strings.CutPrefix(p, module+"/"); ok {
			into[dep] = true
		}
	}
	return nil
}

// ignored reports whether f's build constraint, which must precede the
// package clause, is exactly "ignore".
func ignored(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			tag, ok := expr.(*constraint.TagExpr)
			return err == nil && ok && tag.Tag == "ignore"
		}
	}
	return false
}

// ruleFor returns the exact row for pkg, else the row with the longest
// matching "/..." pattern. Like the go tool's "./...", a pattern matches its
// base package too (internal/api holds the go:generate directives).
func ruleFor(rules []importRule, pkg string) (importRule, bool) {
	var best importRule
	found := false
	for _, r := range rules {
		if r.pkg == pkg {
			return r, true
		}
		if base, ok := strings.CutSuffix(r.pkg, "/..."); ok && (pkg == base || strings.HasPrefix(pkg, base+"/")) && len(base) > len(best.pkg) {
			best, found = importRule{pkg: base, imports: r.imports}, true
		}
	}
	return best, found
}

func allowed(r importRule, pkg, dep string) bool {
	self := ""
	if rest, ok := strings.CutPrefix(pkg, "internal/service/"); ok {
		self, _, _ = strings.Cut(rest, "/")
	}
	for _, a := range r.imports {
		a = strings.ReplaceAll(a, "{self}", self)
		if base, ok := strings.CutSuffix(a, "/..."); ok {
			if dep == base || strings.HasPrefix(dep, base+"/") {
				return true
			}
		} else if dep == a {
			return true
		}
	}
	return false
}

func hint(r importRule, pkg, dep string) string {
	switch {
	case strings.HasPrefix(pkg, "internal/service/") && strings.HasPrefix(dep, "internal/service/"):
		return "a service reaches another through kernel.Invoke with the generated Op"
	case len(r.imports) == 0:
		return "it may import nothing in the repository"
	default:
		return "it may import " + strings.Join(r.imports, ", ")
	}
}

// moduleRoot walks up from the test's directory to go.mod and returns that
// directory and the module path.
func moduleRoot(t *testing.T) (root, module string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for line := range strings.Lines(string(data)) {
				if module, ok := strings.CutPrefix(line, "module "); ok {
					return dir, strings.TrimSpace(module)
				}
			}
			t.Fatalf("%s/go.mod has no module line", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
