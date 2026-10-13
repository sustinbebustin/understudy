package sns

import (
	"encoding/base64"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// pageSize is how many topics or subscriptions one List call returns
// (sns.md 2.2, 3.4).
const pageSize = 100

// page returns the page of items a NextToken names and the token of the
// next page, nil after the last. A token is the offset of its page's first
// item, base64 so clients treat it as opaque; a token that is not one is
// "Invalid parameter: NextToken" (inferred text).
func page[T any](items []T, token *string) (got []T, next *string, err *awserr.Error) {
	start := 0
	if token != nil {
		raw, err := base64.RawURLEncoding.DecodeString(*token)
		if err != nil {
			return nil, nil, errInvalidParameter("NextToken")
		}
		n, err := strconv.Atoi(string(raw))
		if err != nil || n < 0 || n > len(items) {
			return nil, nil, errInvalidParameter("NextToken")
		}
		start = n
	}
	end := min(start+pageSize, len(items))
	if end < len(items) {
		next = new(base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end))))
	}
	return items[start:end], next, nil
}
