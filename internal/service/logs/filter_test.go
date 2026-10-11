package logs_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
)

// corpusDoc holds the filter-pattern vector corpus, section 8.
const corpusDoc = "../../../docs/research/phase1/logs-filter-patterns.md"

// corpusRows is the number of rows section 8 holds; a parse that finds
// fewer has stopped reading the table.
const corpusRows = 167

// fixtures are the event messages section 8 names by id, written out in
// full: the document's fixture table abbreviates some of them.
var fixtures = map[string]string{
	"a1": "[ERROR 400] BAD REQUEST",
	"a2": "[ERROR 401] UNAUTHORIZED REQUEST",
	"a3": "[ERROR 419] MISSING ARGUMENTS",
	"a4": "[ERROR 420] INVALID ARGUMENTS",
	"a5": "[ERROR 500] INTERNAL SERVER ERROR",
	"a6": "[SUCCESS 200] AUTHORIZED REQUEST",
	"a7": "[INFO] REQUEST FAILED",
	"a8": "[WARN] UNAUTHORIZED REQUEST",
	"a9": "[ERROR] 400 BAD REQUEST",

	"b1": "02 May 2014 00:34:12,525 [INFO] Starting the application",
	"b4": "02 May 2014 00:34:16,142 [ERROR] Unhanded exception: InvalidQueryException",
	"b5": "02 May 2014 00:34:16,224 [ERROR] Terminating the application",

	"j1":  `{"eventType": "UpdateTrail", "sourceIPAddress": "111.111.111.111", "arrayKey": ["value", "another value"], "objectList": [{"name": "a", "id": 1}, {"name": "b", "id": 2}], "SomeObject": null, "cluster.name": "c"}`,
	"j2":  `{"user": {"id": 1, "email": "John.Stiles@example.com"}, "users": [{"id": 2, "email": "John.Doe@example.com"}, {"id": 3, "email": "Jane.Doe@example.com"}], "actions": ["GET", "PUT", "DELETE"], "coordinates": [[0, 1, 2], [4, 5, 6], [7, 8, 9]]}`,
	"j3":  `{"latency": 50, "requestType": "GET"}`,
	"j4":  `{"errorCode": "Client.UnauthorizedOperation"}`,
	"j5":  `{"errorCode": "AccessDenied"}`,
	"j6":  `{"errorCode": "NoAccessDenied"}`,
	"j7":  `{"userIdentity": {"type": "Root"}, "eventType": "AwsApiCall"}`,
	"j8":  `{"userIdentity": {"type": "Root", "invokedBy": "x"}, "eventType": "AwsApiCall"}`,
	"j9":  `{"fieldName": "myQuery", "fieldInError": true}`,
	"j10": `{"fieldName": "myQuery", "fieldInError": false}`,

	"s1": `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`,
	"s2": `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 500 1534`,
	"s3": `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 500`,

	"ap1": `127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`,
	"ap2": `127.0.0.1 - frank [10/Oct/2000:13:35:22 -0700] "GET /apache_pb.gif HTTP/1.0" 500 5324`,
	"ap3": `127.0.0.1 - frank [10/Oct/2000:13:50:35 -0700] "GET /apache_pb.gif HTTP/1.0" 200 4355`,

	"h1": `127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534`,
	"h2": `127.0.0.1 - frank [10/Oct/2000:13:35:22 -0700] "GET /about-us/index.html HTTP/1.0" 200 5324`,
	"h3": `127.0.0.1 - frank [10/Oct/2000:13:50:35 -0700] "GET /apache_pb.gif HTTP/1.0" 404 4355`,
	"h4": `127.0.0.1 - frank [10/Oct/2000:13:51:35 -0700] "GET /products/index.html HTTP/1.0" 400 1534`,

	"c1": `127.0.0.1 - bob [10/Oct/2000:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 404 2326`,
	"c2": `127.0.0.1 - bob [10/Oct/2000:13:55:36 -0700] "GET /apache_pb2.gif HTTP/1.0" 200 2326`,

	"w1": "INFO 09/25/2014 12:00:00 GET /service/resource/67 1200",
	"w2": "INFO 09/25/2014 12:00:01 POST /service/resource/67/part/111 1310",
	"w3": "WARNING 09/25/2014 12:00:02 Invalid user request",
	"w4": "ERROR 09/25/2014 12:00:02 Failed to process request",

	"f1": "2 123456789010 eni-1235b8ca123456789 172.31.9.69 172.31.9.12 49761 22 6 20 4249 1418530010 1418530070 REJECT OK",
	"f2": "2 123456789010 eni-1235b8ca123456789 172.31.16.139 172.31.16.21 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK",
	"f3": "2 123456789010 eni-1235b8ca123456789 172.31.9.69 172.31.9.12 49761 3389 6 20 4249 1418530010 1418530070 REJECT OK",

	"t1":  "ERRORS",
	"t2":  "ERROR: disk full",
	"t3":  "[ERROR]",
	"t4":  "MY_ERROR",
	"t5":  "MYERROR",
	"t6":  "ERROR_CODE=5",
	"t7":  "ERROR-5",
	"t8":  "Errorlog has been reinitialized.",
	"t9":  "xERROR",
	"t10": "ERROR",
	"t11": "INTERNAL SERVERS",
	"t12": "INTERNAL SERVER, ERROR",

	"tok": `{"body": "{\"SubscribeURL\" : \"https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:aws:sns:us-east-1:123456789012:t&Token=2336412f37fb687f5d51e6e2\"}"}`,
}

// vector is one row of the corpus: a pattern, the one event it is run
// against and whether FilterLogEvents returns it, returns nothing or
// rejects the pattern.
type vector struct {
	id, pattern, message, want string
}

var (
	corpusRow = regexp.MustCompile(`^\| ([TBJDP]\d{2}) \|`)
	codeSpan  = regexp.MustCompile("^`(.*)`$")
	repeated  = regexp.MustCompile("^`(.)` x (\\d+)$")
)

// readCorpus parses the vector table of section 8 as the document's
// conventions describe it: escaped pipes inside code spans, "(empty)" and
// "(one space)" for blank patterns, "`X` x N" for a repeated character, and
// an event that is a fixture id or a literal message.
func readCorpus(t *testing.T) []vector {
	t.Helper()
	doc, err := os.ReadFile(corpusDoc)
	if err != nil {
		t.Fatal(err)
	}
	var out []vector
	for line := range strings.Lines(string(doc)) {
		if !corpusRow.MatchString(line) {
			continue
		}
		escaped := strings.ReplaceAll(strings.TrimSpace(line), `\|`, "\x00")
		cells := strings.Split(strings.Trim(escaped, "|"), "|")
		for i, c := range cells {
			cells[i] = strings.ReplaceAll(strings.TrimSpace(c), "\x00", "|")
		}
		v := vector{id: cells[0], pattern: corpusPattern(t, cells[1]), want: cells[3]}
		event := codeSpan.FindStringSubmatch(cells[2])
		if event == nil {
			t.Fatalf("%s: event cell %q is not a code span", v.id, cells[2])
		}
		v.message = event[1]
		if m, ok := fixtures[event[1]]; ok {
			v.message = m
		}
		out = append(out, v)
	}
	if len(out) != corpusRows {
		t.Fatalf("read %d corpus rows, want %d", len(out), corpusRows)
	}
	return out
}

func corpusPattern(t *testing.T, cell string) string {
	t.Helper()
	switch cell {
	case "(empty)":
		return ""
	case "(one space)":
		return " "
	}
	if m := repeated.FindStringSubmatch(cell); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Repeat(m[1], n)
	}
	m := codeSpan.FindStringSubmatch(cell)
	if m == nil {
		t.Fatalf("pattern cell %q is not a code span", cell)
	}
	return m[1]
}

// Every vector of logs-filter-patterns.md section 8 gives the same answer
// through FilterLogEvents: the row's event alone in a stream is returned
// for "match", not returned for "no match", and the pattern is rejected
// with InvalidParameterException for "error".
func TestFilterLogEventsMatchesTheVectorCorpus(t *testing.T) {
	corpus := readCorpus(t)
	run(t, func(h *harness) {
		h.group("corpus")
		now := h.now()
		for _, v := range corpus {
			h.stream("corpus", v.id)
			h.put("corpus", v.id, event(now, v.message))
		}
		for _, v := range corpus {
			out, err := call(h, logsapi.OpFilterLogEvents, &logsapi.FilterLogEventsInput{
				LogGroupName: new("corpus"), LogStreamNames: []string{v.id}, FilterPattern: new(v.pattern),
			})
			var got string
			switch {
			case err != nil && err.ID.JSONType == "InvalidParameterException":
				got = "error"
			case err != nil:
				h.Errorf("%s %q: %s %s", v.id, v.pattern, err.ID.JSONType, err.Message)
				continue
			case len(out.Events) == 1 && val(out.Events[0].Message) == v.message:
				got = "match"
			case len(out.Events) == 0:
				got = "no match"
			default:
				h.Errorf("%s %q: %d events", v.id, v.pattern, len(out.Events))
				continue
			}
			if got != v.want {
				h.Errorf("%s: pattern %q on %q = %s, want %s", v.id, v.pattern, v.message, got, v.want)
			}
		}
	})
}
