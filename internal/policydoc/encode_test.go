package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

func TestEncodeIsCanonical(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			// The recorded Lambda permission statement (lambda-api.md 7.3)
			// keeps its element order; Action and Resource stay strings.
			name: "lambda permission",
			in: `{"Statement":{"Condition":{"StringEquals":{"AWS:SourceAccount":"111111111111"},"ArnLike":{"AWS:SourceArn":"arn:aws:sns:us-east-1:111111111111:t"}},
 "Resource":"arn:aws:lambda:us-east-1:111111111111:function:fn","Action":"lambda:InvokeFunction","Principal":{"Service":"sns.amazonaws.com"},"Effect":"Allow","Sid":"sns"},
 "Id":"default","Version":"2012-10-17"}`,
			want: `{"Version":"2012-10-17","Id":"default","Statement":[{"Sid":"sns","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},` +
				`"Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:111111111111:function:fn",` +
				`"Condition":{"ArnLike":{"AWS:SourceArn":"arn:aws:sns:us-east-1:111111111111:t"},"StringEquals":{"AWS:SourceAccount":"111111111111"}}}]}`,
		},
		{
			name: "lists, negations and value kinds",
			in: `{"Statement":[{"Effect":"Deny","NotPrincipal":{"Service":["b","a"],"AWS":"*"},"NotAction":["s3:*"],"NotResource":[],
 "Condition":{"NumericLessThan":{"n":[1.50]},"Bool":{"b":true},"StringLike":{"s":["x","<&>\"\\\u0001é"]}}},{"Effect":"Allow","Principal":"*"}]}`,
			want: `{"Statement":[{"Effect":"Deny","NotPrincipal":{"AWS":"*","Service":["b","a"]},"NotAction":"s3:*","NotResource":[],` +
				`"Condition":{"Bool":{"b":true},"NumericLessThan":{"n":1.50},"StringLike":{"s":["x","<&>\"\\\u0001é"]}}},{"Effect":"Allow","Principal":"*"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := policydoc.Parse([]byte(tc.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := string(policydoc.Encode(doc)); got != tc.want {
				t.Errorf("Encode =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
