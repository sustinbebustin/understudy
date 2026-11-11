package kinesis

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

const (
	// shardPage is the most shards one ListShards page holds (aws-doc
	// kinesis.md 4.6).
	shardPage = 1000
	// listTokenTTL is a ListShards NextToken's lifetime on the emulator
	// clock: "Tokens expire after 300 seconds" (aws-doc kinesis.md 4.6,
	// KIN-11).
	listTokenTTL = 300 * time.Second
)

// bindShards binds ListShards.
func (s *Service) bindShards(b kinesisapi.Binder) {
	b.ListShards(s.listShards, registry.Partial{Note: "the errors for NextToken combined with StreamName, StreamCreationTimestamp or ExclusiveStartShardId, or for neither stream nor token, follow kinesalite and are UNVERIFIED (kinesis.md 4.6)"})
}

// shardListing is what one ListShards listing selects, and what its
// NextToken carries to resume it: the stream by name and creation
// instant (a re-created stream is another stream), the shards after
// After, and, for AT_TIMESTAMP, only when the shards were open by
// OpenedBy.
type shardListing struct {
	Stream   string         `json:"s"`
	Created  clock.Instant  `json:"c"`
	After    string         `json:"a,omitempty"`
	OpenedBy *clock.Instant `json:"o,omitempty"`
}

// listShardsToken is a NextToken's signed payload.
type listShardsToken struct {
	Listing shardListing  `json:"l"`
	Issued  clock.Instant `json:"i"`
}

// listShards answers ListShards: the exclusivity refusals
// first, then either the listing a NextToken resumes or a new one by
// stream and filter, paged min(MaxResults or 1000, 1000).
func (s *Service) listShards(_ context.Context, c *kernel.Call, in *kinesisapi.ListShardsInput) (*kinesisapi.ListShardsOutput, *awserr.Error) {
	if err := listShardsExclusive(in); err != nil {
		return nil, err
	}
	now := c.Admitted()
	var (
		l   shardListing
		str Stream
		err *awserr.Error
	)
	if in.NextToken != nil {
		l, str, err = s.resumeListing(c, *in.NextToken, now)
	} else {
		l, str, err = s.newListing(c, in)
	}
	if err != nil {
		return nil, err
	}
	n := shardPage
	if in.MaxResults != nil {
		n = min(int(*in.MaxResults), shardPage)
	}
	out := &kinesisapi.ListShardsOutput{Shards: []kinesisapi.Shard{}}
	if l.OpenedBy != nil && !str.openedBy(*l.OpenedBy) {
		return out, nil
	}
	for i := range str.Shards {
		if shardID(i) <= l.After {
			continue
		}
		if len(out.Shards) == n {
			next := l
			next.After = out.Shards[n-1].ShardId
			tok, err := s.encodeListToken(c, listShardsToken{Listing: next, Issued: now})
			if err != nil {
				return nil, err
			}
			out.NextToken = &tok
			break
		}
		out.Shards = append(out.Shards, s.shardOf(str, i))
	}
	return out, nil
}

// listShardsExclusive refuses a NextToken with any member that starts a
// listing, and a call with neither (kinesalite's texts for StreamName and
// neither, understudy's for the rest; UNVERIFIED, KIN-15, kinesis.md 4.6).
func listShardsExclusive(in *kinesisapi.ListShardsInput) *awserr.Error {
	if in.NextToken == nil {
		if in.StreamName == nil && in.StreamARN == nil {
			return kinesisapi.ErrInvalidArgumentException("Either NextToken or StreamName should be provided.")
		}
		return nil
	}
	with := ""
	switch {
	case in.StreamName != nil || in.StreamARN != nil:
		with = "StreamName"
	case in.StreamCreationTimestamp != nil:
		with = "StreamCreationTimestamp"
	case in.ExclusiveStartShardId != nil:
		with = "ExclusiveStartShardId"
	case in.ShardFilter != nil:
		with = "ShardFilter"
	default:
		return nil
	}
	return kinesisapi.ErrInvalidArgumentException("NextToken and " + with + " cannot be provided together.")
}

// newListing starts a listing: the filter's required member (understudy's
// texts, KIN-15), the stream, its creation timestamp to the
// millisecond when given (another one is not found), then the start: the
// later of ExclusiveStartShardId and AFTER_SHARD_ID's ShardId. Every shard
// of slice 1 is open and opened with its stream (kinesis.md 4.6, 13.5), so
// FROM_TIMESTAMP and the trim-horizon and latest filters list every shard
// and AT_TIMESTAMP those opened by its Timestamp.
func (s *Service) newListing(c *kernel.Call, in *kinesisapi.ListShardsInput) (shardListing, Stream, *awserr.Error) {
	var l shardListing
	if f := in.ShardFilter; f != nil {
		switch f.Type {
		case kinesisapi.ShardFilterTypeAfterShardId:
			if f.ShardId == nil {
				return l, Stream{}, kinesisapi.ErrInvalidArgumentException("ShardId must be specified for ShardFilter type AFTER_SHARD_ID.")
			}
			l.After = *f.ShardId
		case kinesisapi.ShardFilterTypeAtTimestamp, kinesisapi.ShardFilterTypeFromTimestamp:
			if f.Timestamp == nil {
				return l, Stream{}, kinesisapi.ErrInvalidArgumentException("Timestamp must be specified for ShardFilter type " + string(f.Type) + ".")
			}
			if f.Type == kinesisapi.ShardFilterTypeAtTimestamp {
				l.OpenedBy = new(clock.FromWire(*f.Timestamp))
			}
		case kinesisapi.ShardFilterTypeAtTrimHorizon, kinesisapi.ShardFilterTypeFromTrimHorizon, kinesisapi.ShardFilterTypeAtLatest:
		}
	}
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return l, Stream{}, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return l, Stream{}, err
	}
	if ts := in.StreamCreationTimestamp; ts != nil && millis(clock.FromWire(*ts)) != millis(str.Created) {
		return l, Stream{}, r.notFound()
	}
	if in.ExclusiveStartShardId != nil {
		l.After = max(l.After, *in.ExclusiveStartShardId)
	}
	l.Stream, l.Created = str.Name, str.Created
	return l, str, nil
}

// resumeListing decodes a NextToken: one that does not decode or verify
// under the scope's key is "Invalid NextToken."; one at or past its 300
// seconds is ExpiredNextTokenException (understudy's texts, KIN-11,
// KIN-15); a stream deleted or re-created since is not found.
func (s *Service) resumeListing(c *kernel.Call, token string, now clock.Instant) (shardListing, Stream, *awserr.Error) {
	scope := c.Scope()
	key := store.View(s.cells, scope, func(st *State) []byte { return st.TokenKey })
	raw, err := base64.StdEncoding.Strict().DecodeString(token)
	if key == nil || err != nil || len(raw) <= sha256.Size {
		return shardListing{}, Stream{}, errInvalidNextToken()
	}
	body, mac := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	var t listShardsToken
	if !hmac.Equal(mac, signListToken(key, body)) || json.Unmarshal(body, &t) != nil {
		return shardListing{}, Stream{}, errInvalidNextToken()
	}
	if now.Before(t.Issued) || now.Compare(t.Issued.Add(listTokenTTL)) >= 0 {
		return shardListing{}, Stream{}, kinesisapi.ErrExpiredNextTokenException("The NextToken has expired: ListShards tokens are valid for 300 seconds.")
	}
	r := streamRef{name: t.Listing.Stream, account: scope.Account.String()}
	str, aerr := s.readStream(c, r)
	if aerr != nil {
		return shardListing{}, Stream{}, aerr
	}
	if str.Created != t.Listing.Created {
		return shardListing{}, Stream{}, r.notFound()
	}
	return t.Listing, str, nil
}

// encodeListToken signs t with the scope's key: standard base64 of the
// JSON payload and its HMAC-SHA256.
func (s *Service) encodeListToken(c *kernel.Call, t listShardsToken) (string, *awserr.Error) {
	key := store.View(s.cells, c.Scope(), func(st *State) []byte { return st.TokenKey })
	if key == nil {
		// CreateStream draws the key, so a scope holding a stream has one.
		return "", awserr.Internalf(errors.New("sign a ListShards token: the scope has no token key"))
	}
	body, err := json.Marshal(t)
	if err != nil {
		return "", awserr.Internalf(fmt.Errorf("sign a ListShards token: %w", err))
	}
	return base64.StdEncoding.EncodeToString(append(body, signListToken(key, body)...)), nil
}

// signListToken is the HMAC of a ListShards token payload. The label keeps
// it from verifying as a shard iterator signed with the same key.
func signListToken(key, body []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("kinesis.ListShards\x00"))
	m.Write(body)
	return m.Sum(nil)
}
