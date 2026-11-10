package coverage_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readmeServices maps each row of README.md's Services table to the
// docs/coverage.md services it sums: the README counts a service once
// where AWS models it as two.
var readmeServices = map[string][]string{
	"SQS":                              {"sqs"},
	"SNS":                              {"sns"},
	"S3":                               {"s3"},
	"DynamoDB (with Streams)":          {"dynamodb", "dynamodbstreams"},
	"Kinesis Data Streams":             {"kinesis"},
	"Lambda":                           {"lambda"},
	"CloudWatch Logs":                  {"logs"},
	"Secrets Manager":                  {"secretsmanager"},
	"KMS":                              {"kms"},
	"IAM":                              {"iam"},
	"STS":                              {"sts"},
	"EventBridge":                      {"eventbridge"},
	"EventBridge Scheduler":            {"scheduler"},
	"CloudWatch":                       {"cloudwatch"},
	"API Gateway (HTTP and REST APIs)": {"apigatewayv2", "apigateway"},
	"SSM (Parameter Store)":            {"ssm"},
	"Step Functions":                   {"sfn"},
	"SES (v1 and v2)":                  {"ses", "sesv2"},
}

// serviceCounts is one row's Operations, Full, Stored only, Partial and
// Unbound cells.
type serviceCounts [5]int

// serviceTable reads the first table of path whose header starts with the
// Services columns, keyed by its first cell.
func serviceTable(t *testing.T, path string) map[string]serviceCounts {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const header = "| Service | Operations | Full | Stored only | Partial | Unbound |"
	_, rest, found := strings.Cut(string(data), header)
	if !found {
		t.Fatalf("%s has no table headed %q", path, header)
	}
	rows := map[string]serviceCounts{}
	for i, line := range strings.Split(rest, "\n")[1:] {
		if i == 0 {
			continue // the |---| rule
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 6 {
			t.Fatalf("%s: row %q has %d cells, want at least 6", path, line, len(cells))
		}
		var counts serviceCounts
		for j := range counts {
			n, err := strconv.Atoi(strings.TrimSpace(cells[j+1]))
			if err != nil {
				t.Fatalf("%s: row %q: %v", path, line, err)
			}
			counts[j] = n
		}
		rows[strings.TrimSpace(cells[0])] = counts
	}
	return rows
}

// README.md's Services table restates docs/coverage.md, which
// `just coverage-check` regenerates; the README's counts are written by hand.
func TestReadmeServicesTableMatchesCoverage(t *testing.T) {
	root := filepath.Join("..", "..")
	readme := serviceTable(t, filepath.Join(root, "README.md"))
	doc := serviceTable(t, filepath.Join(root, "docs", "coverage.md"))

	listed := map[string]bool{}
	for label, services := range readmeServices {
		var want serviceCounts
		for _, s := range services {
			counts, ok := doc[s]
			if !ok {
				t.Errorf("docs/coverage.md has no service %q, which readmeServices sums into %q", s, label)
			}
			for j := range want {
				want[j] += counts[j]
			}
			listed[s] = true
		}
		got, ok := readme[label]
		switch {
		case !ok:
			t.Errorf("README.md's Services table has no row %q; add it with counts %v", label, want)
		case got != want:
			t.Errorf("README.md's Services row %q reads %v; docs/coverage.md sums to %v (Operations, Full, Stored only, Partial, Unbound)", label, got, want)
		}
	}
	for label := range readme {
		if _, ok := readmeServices[label]; !ok {
			t.Errorf("README.md's Services row %q is not in readmeServices; add it there with the docs/coverage.md services it sums", label)
		}
	}
	for s := range doc {
		if !listed[s] {
			t.Errorf("docs/coverage.md service %q is in no README.md Services row; add it to the table and to readmeServices", s)
		}
	}
}
