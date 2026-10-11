package iam_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
)

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

// policyText draws a valid identity policy in an arbitrary layout: white
// space between every token, a statement or a list of them, and condition
// values of any character IAM's document charset allows (tab, newline,
// carriage return and U+0020 to U+00FF), "+" and "%" included.
func policyText(t *rapid.T) string {
	ws := func(label string) string {
		return strings.Join(rapid.SliceOfN(rapid.SampledFrom([]string{" ", "\n", "\t", "\r\n", "  "}), 0, 3).Draw(t, label), "")
	}
	charset := rapid.OneOf(rapid.RuneFrom([]rune{'\t', '\n', '\r', '+', '%', ' '}), rapid.Rune().Filter(func(r rune) bool { return r >= 0x20 && r <= 0xFF }))
	value, err := json.Marshal(rapid.StringOfN(charset, 0, 20, -1).Draw(t, "value"))
	if err != nil {
		t.Fatal(err)
	}
	action := rapid.SampledFrom([]string{`"s3:GetObject"`, `["logs:PutLogEvents","logs:CreateLogStream"]`, `"sqs:*"`}).Draw(t, "action")
	statement := fmt.Sprintf(`{%s"Effect"%s:%s"Allow",%s"Action":%s,"Resource":%s"*",%s"Condition":{"StringLike":{"aws:username":%s}}%s}`,
		ws("w1"), ws("w2"), ws("w3"), ws("w4"), action, ws("w5"), ws("w6"), value, ws("w7"))
	if rapid.Bool().Draw(t, "list") {
		statement = "[" + ws("w8") + statement + ws("w9") + "]"
	}
	return fmt.Sprintf(`%s{%s"Version":%s"2012-10-17",%s"Statement":%s%s}%s`, ws("w10"), ws("w11"), ws("w12"), ws("w13"), statement, ws("w14"), ws("w15"))
}

// Every document IAM accepts reads back byte for byte: GetRolePolicy,
// GetUserPolicy and GetPolicyVersion return the text that was sent, which
// Terraform compares with its configuration.
func TestPolicyDocumentsRoundTripByteStable(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		n := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			n++
			doc := policyText(t)
			must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "p", PolicyDocument: doc})
			if got := must(h, iamapi.OpGetRolePolicy, &iamapi.GetRolePolicyInput{RoleName: "r", PolicyName: "p"}).PolicyDocument; got != doc {
				t.Fatalf("GetRolePolicy = %q, put %q", got, doc)
			}
			must(h, iamapi.OpPutUserPolicy, &iamapi.PutUserPolicyInput{UserName: "u", PolicyName: "p", PolicyDocument: doc})
			if got := must(h, iamapi.OpGetUserPolicy, &iamapi.GetUserPolicyInput{UserName: "u", PolicyName: "p"}).PolicyDocument; got != doc {
				t.Fatalf("GetUserPolicy = %q, put %q", got, doc)
			}
			arn := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: fmt.Sprintf("p%d", n), PolicyDocument: doc}).Policy.Arn
			if got := *must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: arn, VersionId: "v1"}).PolicyVersion.Document; got != doc {
				t.Fatalf("GetPolicyVersion = %q, created %q", got, doc)
			}
		})
	})
}

// ListRoles pages, followed by Marker until IsTruncated is false,
// concatenate to the unpaginated listing: every role under the path prefix,
// by name regardless of case.
func TestListRolesPagesConcatenateToTheListing(t *testing.T) {
	run(t, func(h *harness) {
		accounts := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			accounts++
			h.Scope.Account = mustAccount(h, fmt.Sprintf("%012d", accounts))
			names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Za-z][a-z0-9]{0,5}`), 0, 12, strings.ToLower).Draw(t, "names")
			paths := map[string]string{}
			for _, name := range names {
				paths[name] = rapid.SampledFrom([]string{"/", "/a/", "/a/b/", "/b/"}).Draw(t, "path")
				must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: name, Path: new(paths[name]), AssumeRolePolicyDocument: trustLambda})
			}
			prefix := rapid.SampledFrom([]string{"", "/", "/a/", "/b/"}).Draw(t, "prefix")
			var want []string
			for _, name := range names {
				if strings.HasPrefix(paths[name], prefix) {
					want = append(want, name)
				}
			}
			slices.SortFunc(want, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })

			in := iamapi.ListRolesInput{MaxItems: new(rapid.Int32Range(1, 5).Draw(t, "maxItems"))}
			if prefix != "" {
				in.PathPrefix = new(prefix)
			}
			var got []string
			for range len(names) + 2 {
				out := must(h, iamapi.OpListRoles, &in)
				for _, r := range out.Roles {
					got = append(got, r.RoleName)
				}
				if !out.IsTruncated {
					break
				}
				in.Marker = out.Marker
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pages = %v, listing = %v", got, want)
			}
		})
	})
}
