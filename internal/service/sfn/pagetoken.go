package sfn

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// tokenLife is how long a page token stays valid on the emulator clock
// (stepfunctions-api.md 4.3).
const tokenLife = 24 * time.Hour

// defaultPageSize is the page size of a maxResults of 0
// (stepfunctions-api.md 4.3).
const defaultPageSize = 100

// pageSize is maxResults, with 0 meaning the default.
func pageSize(maxResults int32) int {
	if maxResults <= 0 {
		return defaultPageSize
	}
	return int(maxResults)
}

// pageToken is the token that continues listing after position: the
// listing it belongs to, the position and the instant it was issued, so a
// token of another listing, or one older than tokenLife, is refused.
func pageToken(listing, position string, issued clock.Instant) string {
	body := listing + "\x00" + position + "\x00" + strconv.FormatInt(issued.Wire().UnixMilli(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(body))
}

// readToken returns the position token continues listing at.
func readToken(token, listing string, now clock.Instant) (string, *awserr.Error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", errInvalidToken()
	}
	// The listing may hold the separator, so the position and the issue
	// instant are read from the end.
	rest, stamp, ok := strings.CutLast(string(raw), "\x00")
	if !ok {
		return "", errInvalidToken()
	}
	body, position, ok := strings.CutLast(rest, "\x00")
	if !ok || body != listing {
		return "", errInvalidToken()
	}
	ms, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || now.Sub(clock.FromWire(time.UnixMilli(ms))) >= tokenLife {
		return "", errInvalidToken()
	}
	return position, nil
}

// errInvalidToken is the InvalidToken text SFN-05 recorded 2026-11-03 for a
// bad GetExecutionHistory token, with no subject; the list operations are
// inferred to answer the same (stepfunctions-api.md 3.2).
func errInvalidToken() *awserr.Error {
	return sfnapi.ErrInvalidToken("Invalid Token")
}
