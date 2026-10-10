package logsfilter

import (
	"strings"
	"testing"
)

// fixtures are the event messages of logs-filter-patterns.md section 8.
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

// outcome is the result of one pattern on one event: a match, no match, or
// a rejection for one Cause.
type outcome int

const (
	match outcome = iota
	noMatch
	badLength
	badTerm
	badSyntax
	// badUnknownCause is an Error without a known Cause; no row expects it.
	badUnknownCause
)

func (o outcome) String() string {
	switch o {
	case match:
		return "match"
	case noMatch:
		return "no match"
	case badLength:
		return "error: " + CauseTooLong.String()
	case badTerm:
		return "error: " + CauseInvalidTerm.String()
	case badSyntax:
		return "error: " + CauseSyntax.String()
	case badUnknownCause:
	}
	return "error: unknown cause"
}

// vector is one row of logs-filter-patterns.md section 8. event is a fixture
// id when it names one, otherwise the literal message.
type vector struct {
	id, pattern, event string
	want               outcome
}

func (v vector) message() string {
	if m, ok := fixtures[v.event]; ok {
		return m
	}
	return v.event
}

// corpus holds every row of logs-filter-patterns.md section 8. The expected
// column assumes the default term boundary (substring) and the default field
// count (the last field absorbs the rest).
var corpus = []vector{
	{"T01", `ERROR`, "a1", match},
	{"T02", `ERROR`, "a3", match},
	{"T03", `ERROR`, "a6", noMatch},
	{"T04", `ERROR ARGUMENTS`, "a3", match},
	{"T05", `ERROR ARGUMENTS`, "a4", match},
	{"T06", `ERROR ARGUMENTS`, "a1", noMatch},
	{"T07", `ERROR ARGUMENTS`, "a2", noMatch},
	{"T08", `ARGUMENTS ERROR`, "a3", match},
	{"T09", `?ERROR ?ARGUMENTS`, "a1", match},
	{"T10", `?ERROR ?ARGUMENTS`, "a3", match},
	{"T11", `?ERROR ?ARGUMENTS`, "a2", match},
	{"T12", `?ERROR ?ARGUMENTS`, "a6", noMatch},
	{"T13", `?ERROR ?ARGUMENTS REQUEST`, "a7", match},
	{"T14", `?ERROR ?ARGUMENTS REQUEST`, "a8", match},
	{"T15", `?ERROR ?ARGUMENTS REQUEST`, "a9", match},
	{"T16", `?ERROR ?ARGUMENTS REQUEST`, "a3", noMatch},
	{"T17", `"INTERNAL SERVER ERROR"`, "a5", match},
	{"T18", `"INTERNAL SERVER ERROR"`, "[ERROR 500] INTERNAL ERROR SERVER", noMatch},
	{"T19", `ERROR -ARGUMENTS`, "a1", match},
	{"T20", `ERROR -ARGUMENTS`, "a2", match},
	{"T21", `ERROR -ARGUMENTS`, "a3", noMatch},
	{"T22", `ERROR -ARGUMENTS`, "a4", noMatch},
	{"T23", `?ERROR -ARGUMENTS`, "a6", match},
	{"T24", `-ARGUMENTS`, "a1", match},
	{"T25", `-ARGUMENTS`, "a3", noMatch},
	{"T26", `" "`, "a1", match},
	{"T27", `" "`, "hello", match},
	{"T28", ``, "hello", match},
	{"T29", `%AUTHORIZED%`, "a2", match},
	{"T30", `%AUTHORIZED%`, "a6", match},
	{"T31", `%AUTHORIZED%`, "a1", noMatch},
	{"T32", `%^starting|^initializing|^shutting down%`, "starting up now", match},
	{"T33", `%^starting|^initializing|^shutting down%`, "skipping initializing x", noMatch},
	{"T34", `%colou?r%`, "the colour red", match},
	{"T35", `%[hc]at$%`, "the hat", match},
	{"T36", `%[hc]at$%`, "hat trick", noMatch},
	{"T37", `%10.10.0.1%`, "10010,051", match},
	{"T38", `%10\.10\.0\.1%`, "10010,051", noMatch},
	{"T39", `%10\.10\.0\.1%`, "ip=10.10.0.1 ok", match},
	{"T40", `"[ERROR]" Exception`, "b4", match},
	{"T41", `"[ERROR]" Exception`, "b5", noMatch},
	{"T42", `"[ERROR]" Exception`, "b1", noMatch},
	{"T43", `"[ERROR]"`, "b4", match},
	{"T44", `"[ERROR]"`, "b5", match},
	{"T45", `"[ERROR]"`, "b1", noMatch},
	{"T46", `Error`, "This message contains an Error", match},
	{"T47", `Error`, "Test event 1", noMatch},
	{"T48", `Error`, "an error occurred", noMatch},
	{"T49", `ERROR`, "error: x", noMatch},
	{"T50", `Token=`, "tok", match},
	{"T51", `<INFO>`, "<INFO> started", badTerm},
	{"T52", `"<INFO>"`, "<INFO> started", match},
	{"T53", `"say \"hi\""`, `they say "hi" loudly`, match},
	{"T54", `"Webhook processed successfully"`, "INFO Webhook processed successfully id=7", match},
	{"T55", ` `, "hello", match},
	{"B01", `ERROR`, "t1", match},
	{"B02", `ERROR`, "t2", match},
	{"B03", `ERROR`, "t3", match},
	{"B04", `ERROR`, "t4", match},
	{"B05", `ERROR`, "t5", match},
	{"B06", `ERROR`, "t6", match},
	{"B07", `ERROR`, "t7", match},
	{"B08", `Error`, "t8", match},
	{"B09", `Exception`, "b4", match},
	{"B10", `ERROR`, "t9", match},
	{"B11", `ERR`, "a1", match},
	{"B12", `"INTERNAL SERVER"`, "t11", match},
	{"B13", `"SERVER ERROR"`, "t12", noMatch},
	{"J01", `{ $.eventType = "UpdateTrail" }`, "j1", match},
	{"J02", `{ $.eventType = "UpdateTrails" }`, "j1", noMatch},
	{"J03", `{ $.eventType = "Update*" }`, "j1", match},
	{"J04", `{ $.eventType = "*Trail" }`, "j1", match},
	{"J05", `{ $.eventType = "*" }`, "j1", match},
	{"J06", `{ $.sourceIPAddress != 123.123.* }`, "j1", match},
	{"J07", `{ $.sourceIPAddress != 111.* }`, "j1", noMatch},
	{"J08", `{ $.arrayKey[0] = "value" }`, "j1", match},
	{"J09", `{ $.arrayKey[1] = "value" }`, "j1", noMatch},
	{"J10", `{ $.arrayKey[1] = "another value" }`, "j1", match},
	{"J11", `{ $.eventType = %Trail% }`, "j1", match},
	{"J12", `{ $.eventType = %^Trail% }`, "j1", noMatch},
	{"J13", `{ $.arrayKey[*] = %val.{2}% }`, "j1", match},
	{"J14", `{ $.* = %111\.111\.111\.1[0-9]{1,2}% }`, "j1", match},
	{"J15", `{ $.['cluster.name'] = "c" }`, "j1", match},
	{"J16", `{ $.["cluster.name"] = "c" }`, "j1", badSyntax},
	{"J17", `{ $.SomeObject IS NULL }`, "j1", match},
	{"J18", `{ $.eventType IS NULL }`, "j1", noMatch},
	{"J19", `{ $.SomeOtherObject NOT EXISTS }`, "j1", match},
	{"J20", `{ $.eventType NOT EXISTS }`, "j1", noMatch},
	{"J21", `{ $.SomeObject NOT EXISTS }`, "j1", noMatch},
	{"J22", `{ $.objectList[1].id = 2 }`, "j1", match},
	{"J23", `{ $.objectList = "a" }`, "j1", noMatch},
	{"J24", `{ $.objectList[0].name = "a" && $.objectList[1].id > 1 }`, "j1", match},
	{"J25", `{ $.SomeObject IS NOT NULL }`, "j1", badSyntax},
	{"J26", `{ $.eventType EXISTS }`, "j1", badSyntax},
	{"J27", `{ ($.user.id = 1) && ($.users[0].email = "John.Doe@example.com") }`, "j2", match},
	{"J28", `{ $.user.email = "John.Stiles@example.com" || $.coordinates[0][1] = "nonmatch" && $.actions[2] = "nonmatch" }`, "j2", match},
	{"J29", `{ ($.user.email = "John.Stiles@example.com" || $.coordinates[0][1] = "nonmatch") && $.actions[2] = "nonmatch" }`, "j2", noMatch},
	{"J30", `{ ($.user.id = 2 && $.users[0].email = "nonmatch") || $.actions[2] = "GET" }`, "j2", noMatch},
	{"J31", `{ $.coordinates[0][1] = 1 }`, "j2", match},
	{"J32", `{ $.user.id >= 1 }`, "j2", match},
	{"J33", `{ $.user.id > 1 }`, "j2", noMatch},
	{"J34", `{ $.user.id != 1 }`, "j2", noMatch},
	{"J35", `{ $.latency = * }`, "j3", match},
	{"J36", `{ $.latency > 75 }`, "j3", noMatch},
	{"J37", `{ $.latency <= 50 }`, "j3", match},
	{"J38", `{ $.latency = 5e1 }`, "j3", match},
	{"J39", `{ $.missing = * }`, "j3", noMatch},
	{"J40", `{ $.eventType = "*" && $.sourceIPAddress != 123.123.* }`, "j1", match},
	{"J41", `{ $.userIdentity.type = Root }`, "j7", match},
	{"J42", `{($.errorCode="*UnauthorizedOperation") || ($.errorCode="AccessDenied*")}`, "j4", match},
	{"J43", `{($.errorCode="*UnauthorizedOperation") || ($.errorCode="AccessDenied*")}`, "j5", match},
	{"J44", `{($.errorCode="*UnauthorizedOperation") || ($.errorCode="AccessDenied*")}`, "j6", noMatch},
	{"J45", `{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType !="AwsServiceEvent"}`, "j7", match},
	{"J46", `{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType !="AwsServiceEvent"}`, "j8", noMatch},
	{"J47", `{ $.fieldName = "myQuery" && $.fieldInError IS TRUE }`, "j9", match},
	{"J48", `{ $.fieldName = "myQuery" && $.fieldInError IS TRUE }`, "j10", noMatch},
	{"J49", `{ $.a = 1 }`, "hello world", noMatch},
	{"J50", `{ $.eventType = "UpdateTrail" }`, "xx", noMatch},
	{"D01", `[ip, user, username, timestamp, request, status_code, bytes]`, "s1", match},
	{"D02", `[ip=%127\.0\.0\.[1-9]%, user, username, timestamp, request =*.html*, status_code = 4*, bytes]`, "s1", match},
	{"D03", `[..., request =*.html*, status_code = 4*, bytes]`, "s1", match},
	{"D04", `[ip, user, username, timestamp, request =*.html*, status_code = 404 || status_code = 410, bytes]`, "s1", match},
	{"D05", `[ip, user, username, timestamp, request =*.html*, status_code = 404 || status_code = 410, bytes]`, "s2", noMatch},
	{"D06", `[w1=ERROR, w2]`, "w4", match},
	{"D07", `[w1=ERROR, w2]`, "w3", noMatch},
	{"D08", `[w1=ERROR, w2]`, "w1", noMatch},
	{"D09", `[w1=ERROR || w1=WARNING, w2]`, "w3", match},
	{"D10", `[w1=ERROR || w1=WARNING, w2]`, "w4", match},
	{"D11", `[w1=ERROR || w1=WARNING, w2]`, "w1", noMatch},
	{"D12", `[w1!=ERROR && w1!=WARNING, w2]`, "w1", match},
	{"D13", `[w1!=ERROR && w1!=WARNING, w2]`, "w3", noMatch},
	{"D14", `[w1!=ERROR && w1!=WARNING, w2]`, "w4", noMatch},
	{"D15", `[logLevel, date, time, method, url=%/service/resource/[0-9]+$%, response_time]`, "w1", match},
	{"D16", `[logLevel, date, time, method, url=%/service/resource/[0-9]+$%, response_time]`, "w2", noMatch},
	{"D17", `[logLevel, date, time, method, url=%/service/resource/[0-9]+/part/[0-9]+$%, response_time]`, "w2", match},
	{"D18", `[logLevel, date, time, method, url=%/service/resource/[0-9]+/part/[0-9]+$%, response_time]`, "w1", noMatch},
	{"D19", `[ip, id, user, timestamp, request, status_code=404, size]`, "c1", match},
	{"D20", `[ip, id, user, timestamp, request, status_code=404, size]`, "c2", noMatch},
	{"D21", `[ip, id, user, timestamp, request, status_code=4*, size]`, "h1", match},
	{"D22", `[ip, id, user, timestamp, request, status_code=4*, size]`, "ap1", noMatch},
	{"D23", `[ip, identity, user_id, timestamp, request, status_code, size]`, "ap2", match},
	{"D24", `[..., size]`, "ap1", match},
	{"D25", `[]`, "ap1", match},
	{"D26", `[..., status_code=200, size]`, "ap1", match},
	{"D27", `[..., status_code=200, size]`, "ap2", noMatch},
	{"D28", `[..., status_code=200, size]`, "ap3", match},
	{"D29", `[..., request=*.html*, status_code=4*,]`, "h1", match},
	{"D30", `[..., request=*.html*, status_code=4*,]`, "h2", noMatch},
	{"D31", `[..., request=*.html*, status_code=4*,]`, "h3", noMatch},
	{"D32", `[..., request=*.html*, status_code=4*,]`, "h4", match},
	{"D33", `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]`, "f1", match},
	{"D34", `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]`, "f2", noMatch},
	{"D35", `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]`, "f3", noMatch},
	{"D36", `[ip, server, username, timestamp, request, status_code, bytes > 1000]`, "s1", match},
	{"D37", `[ip, server, username, timestamp, request, status_code, bytes > 1000]`, "s3", noMatch},
	{"D38", `[w1=ERROR || w1=%WARN%, w2]`, "w3", match},
	{"D39", `[w1=ERROR || w1=%WARN%, w2]`, "w1", noMatch},
	{"D40", `[a, b, c]`, "x y", noMatch},
	{"D41", `[f1, f2, f3]`, `"GET /a b" [10/Oct 13:25] x`, match},
	{"D42", `[w1=ERROR, w2]`, "ERRORS 09/25/2014 x", noMatch},
	{"D43", `[ip, id, user, ..., status_code=500, size]`, `127.0.0.1 - frank "GET /apache_pb.gif HTTP/1.0" 500 5324`, match},
	{"D44", `[ip, id, user, ..., status_code=500, size]`, `127.0.0.1 - frank "GET /apache_pb.gif HTTP/1.0" 200 1534`, noMatch},
	{"P01", `%something!%`, "x", badSyntax},
	{"P02", `%(a|b)%`, "a", badSyntax},
	{"P03", `%\x28%`, "a(b", match},
	{"P04", strings.Repeat("X", 1025), "x", badLength},
	{"P05", `{ $.a = 1`, "{}", badSyntax},
}

func evaluate(o Options, pattern, message string) outcome {
	f, err := o.Compile(pattern)
	if err != nil {
		switch err.Cause() {
		case CauseTooLong:
			return badLength
		case CauseInvalidTerm:
			return badTerm
		case CauseSyntax:
			return badSyntax
		}
		return badUnknownCause
	}
	if Match(f, message) {
		return match
	}
	return noMatch
}

func runVectors(t *testing.T, o Options, rows []vector) {
	t.Helper()
	for _, v := range rows {
		t.Run(v.id, func(t *testing.T) {
			t.Parallel()
			if got := evaluate(o, v.pattern, v.message()); got != v.want {
				t.Errorf("pattern %q on %q: got %v, want %v", v.pattern, v.message(), got, v.want)
			}
		})
	}
}

func TestCorpusVectors(t *testing.T) {
	t.Parallel()
	runVectors(t, Options{}, corpus)
}

// flipped returns the corpus rows whose expectation an alternative switch
// value inverts, with the inverted expectation; every other row is unchanged.
func flipped(ids ...string) []vector {
	flip := make(map[string]bool, len(ids))
	for _, id := range ids {
		flip[id] = true
	}
	rows := make([]vector, 0, len(corpus))
	for _, v := range corpus {
		if flip[v.id] {
			switch v.want {
			case match:
				v.want = noMatch
			case noMatch:
				v.want = match
			case badLength, badTerm, badSyntax, badUnknownCause:
			}
		}
		rows = append(rows, v)
	}
	return rows
}

// The token rule T of logs-filter-patterns.md section 5 flips exactly the
// rows listed in section 8.
func TestCorpusUnderTokenBoundary(t *testing.T) {
	t.Parallel()
	rows := flipped("B01", "B04", "B05", "B06", "B08", "B09", "B10", "B11", "B12", "T40", "B13")
	runVectors(t, Options{TermBoundary: TermToken}, rows)
}

// The exact field-count rule flips exactly the rows listed in section 8.
func TestCorpusUnderExactFieldCount(t *testing.T) {
	t.Parallel()
	rows := flipped("D06", "D09", "D10", "D12", "D38")
	runVectors(t, Options{FieldCount: FieldCountExact}, rows)
}

func TestFilterPatternLengthErrorText(t *testing.T) {
	t.Parallel()
	pattern := strings.Repeat("X", 1025)
	_, err := Compile(pattern)
	if err == nil {
		t.Fatal("Compile accepted a 1025 character pattern")
	}
	want := "1 validation error detected: Value '" + pattern +
		"' at 'filterPattern' failed to satisfy constraint: Member must have length less than or equal to 1024"
	if err.Message() != want {
		t.Errorf("message = %q, want %q", err.Message(), want)
	}
	if _, err := Compile(strings.Repeat("X", 1024)); err != nil {
		t.Errorf("Compile rejected a 1024 character pattern: %v", err)
	}
}

func TestInvalidTermCharacterErrorText(t *testing.T) {
	t.Parallel()
	_, err := Compile("<INFO>")
	if err == nil {
		t.Fatal("Compile accepted an unquoted <INFO>")
	}
	if want := "Invalid character(s) in term <INFO>"; err.Message() != want {
		t.Errorf("message = %q, want %q", err.Message(), want)
	}
}

// The corpus rows AWS documents as rejected must fail for the documented
// reason, not an earlier, unrelated one. The texts are this package's own
// (AWS's are unrecorded); the detail and offset pin which rule fired.
func TestDocumentedRejectionsFailForTheDocumentedReason(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ id, pattern, want string }{
		{"J16", `{ $.["cluster.name"] = "c" }`, "Invalid filter pattern: bracket notation takes a single-quoted property name at offset 5."},
		{"J25", `{ $.SomeObject IS NOT NULL }`, "Invalid filter pattern: IS NOT is not supported; use != or NOT EXISTS at offset 15."},
		{"J26", `{ $.eventType EXISTS }`, "Invalid filter pattern: EXISTS is not supported; use = * or NOT EXISTS at offset 14."},
		{"P01", `%something!%`, "Invalid filter pattern: character '!' is not allowed in a regular expression at offset 0."},
		{"P02", `%(a|b)%`, "Invalid filter pattern: character '(' is not allowed in a regular expression at offset 0."},
	} {
		_, err := Compile(c.pattern)
		if err == nil {
			t.Errorf("%s: Compile(%q) accepted", c.id, c.pattern)
			continue
		}
		if err.Cause() != CauseSyntax || err.Message() != c.want {
			t.Errorf("%s: Compile(%q) = %v %q, want %v %q", c.id, c.pattern, err.Cause(), err.Message(), CauseSyntax, c.want)
		}
	}
}

// securityHubPatterns are the 14 CIS metric filter patterns of the Security
// Hub CloudWatch.1 to CloudWatch.14 controls, verbatim from
// https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html.
var securityHubPatterns = []string{
	`{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType !="AwsServiceEvent"}`,
	`{($.errorCode="*UnauthorizedOperation") || ($.errorCode="AccessDenied*")}`,
	`{ ($.eventName = "ConsoleLogin") && ($.additionalEventData.MFAUsed != "Yes") && ($.userIdentity.type = "IAMUser") && ($.responseElements.ConsoleLogin = "Success") }`,
	`{($.eventSource=iam.amazonaws.com) && (($.eventName=DeleteGroupPolicy) || ($.eventName=DeleteRolePolicy) || ($.eventName=DeleteUserPolicy) || ($.eventName=PutGroupPolicy) || ($.eventName=PutRolePolicy) || ($.eventName=PutUserPolicy) || ($.eventName=CreatePolicy) || ($.eventName=DeletePolicy) || ($.eventName=CreatePolicyVersion) || ($.eventName=DeletePolicyVersion) || ($.eventName=AttachRolePolicy) || ($.eventName=DetachRolePolicy) || ($.eventName=AttachUserPolicy) || ($.eventName=DetachUserPolicy) || ($.eventName=AttachGroupPolicy) || ($.eventName=DetachGroupPolicy))}`,
	`{($.eventName=CreateTrail) || ($.eventName=UpdateTrail) || ($.eventName=DeleteTrail) || ($.eventName=StartLogging) || ($.eventName=StopLogging)}`,
	`{($.eventName=ConsoleLogin) && ($.errorMessage="Failed authentication")}`,
	`{($.eventSource=kms.amazonaws.com) && (($.eventName=DisableKey) || ($.eventName=ScheduleKeyDeletion))}`,
	`{($.eventSource=s3.amazonaws.com) && (($.eventName=PutBucketAcl) || ($.eventName=PutBucketPolicy) || ($.eventName=PutBucketCors) || ($.eventName=PutBucketLifecycle) || ($.eventName=PutBucketReplication) || ($.eventName=DeleteBucketPolicy) || ($.eventName=DeleteBucketCors) || ($.eventName=DeleteBucketLifecycle) || ($.eventName=DeleteBucketReplication))}`,
	`{($.eventSource=config.amazonaws.com) && (($.eventName=StopConfigurationRecorder) || ($.eventName=DeleteDeliveryChannel) || ($.eventName=PutDeliveryChannel) || ($.eventName=PutConfigurationRecorder))}`,
	`{($.eventName=AuthorizeSecurityGroupIngress) || ($.eventName=AuthorizeSecurityGroupEgress) || ($.eventName=RevokeSecurityGroupIngress) || ($.eventName=RevokeSecurityGroupEgress) || ($.eventName=CreateSecurityGroup) || ($.eventName=DeleteSecurityGroup)}`,
	`{($.eventName=CreateNetworkAcl) || ($.eventName=CreateNetworkAclEntry) || ($.eventName=DeleteNetworkAcl) || ($.eventName=DeleteNetworkAclEntry) || ($.eventName=ReplaceNetworkAclEntry) || ($.eventName=ReplaceNetworkAclAssociation)}`,
	`{($.eventName=CreateCustomerGateway) || ($.eventName=DeleteCustomerGateway) || ($.eventName=AttachInternetGateway) || ($.eventName=CreateInternetGateway) || ($.eventName=DeleteInternetGateway) || ($.eventName=DetachInternetGateway)}`,
	`{($.eventSource=ec2.amazonaws.com) && (($.eventName=CreateRoute) || ($.eventName=CreateRouteTable) || ($.eventName=ReplaceRoute) || ($.eventName=ReplaceRouteTableAssociation) || ($.eventName=DeleteRouteTable) || ($.eventName=DeleteRoute) || ($.eventName=DisassociateRouteTable))}`,
	`{($.eventName=CreateVpc) || ($.eventName=DeleteVpc) || ($.eventName=ModifyVpcAttribute) || ($.eventName=AcceptVpcPeeringConnection) || ($.eventName=CreateVpcPeeringConnection) || ($.eventName=DeleteVpcPeeringConnection) || ($.eventName=RejectVpcPeeringConnection) || ($.eventName=AttachClassicLinkVpc) || ($.eventName=DetachClassicLinkVpc) || ($.eventName=DisableVpcClassicLink) || ($.eventName=EnableVpcClassicLink)}`,
}

// The parse-only corpus of logs-filter-patterns.md section 4: patterns AWS
// accepts that no documented event exercises.
func TestParseOnlyCorpusCompiles(t *testing.T) {
	t.Parallel()
	patterns := append([]string{
		`{ $.bandwidth > 75 }`,
		`{ $.latency < 50 }`,
		`{ $.refreshRate >= 60 }`,
		`{ $.responseTime <= 5 }`,
		`{ $.errorCode = 400}`,
		`{ $.errorCode != 500 }`,
		`{ $.number[0] = 1e+3 }`,
		`{ $.number[0] != 1e-3 }`,
		`{ $.statusCode=%4[0-9]{2}% }`,
		`{ $.response_code >= 200 }`,
		`[ip, identity, user_id, timestamp, request, status_code = 500, size]`,
		`{ $.log = "*GET /espace-client/selfcare/contract/*" && $.log = "* 200 *" }`,
		`{ $.latency = "*" }`,
		`{ $.field = %value% }`,
		`[..., status_code = "4*" && status_code != 403, bytes]`,
		``,
		` `,
		`[]`,
		`{$.message = "test"}`,
		`pattern`,
		`filterPattern`,
	}, securityHubPatterns...)
	for _, p := range patterns {
		if _, err := Compile(p); err != nil {
			t.Errorf("Compile(%q): %v", p, err)
		}
	}
}
