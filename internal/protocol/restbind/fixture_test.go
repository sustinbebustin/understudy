package restbind_test

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The schema below stands in for a generated S3-like operation
// PUT /{Bucket}/{Key+}: one member per HTTP binding restbind handles. The
// hand-written Deserialize and Serialize follow the generated pattern.

var (
	stringSchema    = &smithy.Schema{ID: "test#String", Type: smithy.TypeString}
	intSchema       = &smithy.Schema{ID: "test#Integer", Type: smithy.TypeInteger}
	boolSchema      = &smithy.Schema{ID: "test#Boolean", Type: smithy.TypeBoolean}
	timestampSchema = &smithy.Schema{ID: "test#Timestamp", Type: smithy.TypeTimestamp}
	blobSchema      = &smithy.Schema{ID: "test#Blob", Type: smithy.TypeBlob}
	streamSchema    = &smithy.Schema{ID: "test#Stream", Type: smithy.TypeBlob, Streaming: true}
	stringList      = &smithy.Schema{ID: "test#StringList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: stringSchema}}}
	timestampList   = &smithy.Schema{ID: "test#TimestampList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: timestampSchema}}}
	stringMap       = &smithy.Schema{ID: "test#StringMap", Type: smithy.TypeMap, Members: []*smithy.Member{
		{Name: "key", Target: stringSchema}, {Name: "value", Index: 1, Target: stringSchema},
	}}

	mBucket = &smithy.Member{Name: "Bucket", Target: stringSchema, Required: true, HTTP: smithy.HTTPLabel{}}
	mKey    = &smithy.Member{Name: "Key", Target: stringSchema, Required: true, HTTP: smithy.HTTPLabel{}}
	mTags   = &smithy.Member{Name: "Tags", Target: stringList, HTTP: smithy.HTTPQuery{Name: "tag"}}
	mLimit  = &smithy.Member{Name: "Limit", Target: intSchema, HTTP: smithy.HTTPQuery{Name: "max-keys"}}
	mWhen   = &smithy.Member{Name: "When", Target: timestampSchema, HTTP: smithy.HTTPHeader{Name: "X-When"}}
	mStamps = &smithy.Member{Name: "Stamps", Target: timestampList, HTTP: smithy.HTTPHeader{Name: "X-Stamps"}}
	mNames  = &smithy.Member{Name: "Names", Target: stringList, HTTP: smithy.HTTPHeader{Name: "X-Names"}}
	mFlag   = &smithy.Member{Name: "Flag", Target: boolSchema, HTTP: smithy.HTTPHeader{Name: "X-Flag"}}
	mMeta   = &smithy.Member{Name: "Meta", Target: stringMap, HTTP: smithy.HTTPPrefixHeaders{Prefix: "x-amz-meta-"}}
	mSum    = &smithy.Member{Name: "Checksum", Target: stringSchema, HTTP: smithy.HTTPHeader{Name: "x-amz-checksum-crc32", Trailer: true}}
	mStatus = &smithy.Member{Name: "Status", Target: intSchema, HTTP: smithy.HTTPResponseCode{}}
	mData   = &smithy.Member{Name: "Data", Target: blobSchema, HTTP: smithy.HTTPPayload{}}
	mBody   = &smithy.Member{Name: "Body", Target: streamSchema, HTTP: smithy.HTTPPayload{}}

	headMembers = []*smithy.Member{mWhen, mStamps, mNames, mFlag, mMeta}

	putInput = &smithy.Schema{
		ID: "test#PutInput", Type: smithy.TypeStructure,
		Members: append([]*smithy.Member{mBucket, mKey, mTags, mLimit}, append(slices.Clone(headMembers), mSum, mData)...),
	}
	putOutput = &smithy.Schema{
		ID: "test#PutOutput", Type: smithy.TypeStructure,
		Members: append(slices.Clone(headMembers), mStatus, mData),
	}
	streamInput = &smithy.Schema{
		ID: "test#StreamInput", Type: smithy.TypeStructure,
		Members: []*smithy.Member{mBucket, mKey, mSum, mBody},
	}
)

// operation returns a registry operation over in and out, routed by uri.
func operation(uri string, in, out *smithy.Schema) *registry.Operation {
	b, err := registry.ParseHTTPBinding(http.MethodPut, uri)
	if err != nil {
		panic(err)
	}
	return &registry.Operation{ID: ref.OperationID{Service: "test", Name: "Put"}, Kind: registry.Modeled, Input: in, Output: out, HTTP: &b}
}

// put is the input and output value of the stand-in operation.
type put struct {
	Bucket, Key string
	Tags        []string
	Limit       *int32
	When        *time.Time
	Stamps      []time.Time
	Names       []string
	Flag        *bool
	Meta        map[string]string
	Checksum    *string
	Status      int32
	Data        []byte
	Body        smithy.Body
}

func (v *put) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(putInput); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		if err := readMember(v, d, m); err != nil {
			return err
		}
	}
}

func readMember(v *put, d smithy.Deserializer, m *smithy.Member) (err error) {
	switch m {
	case mBucket:
		v.Bucket, err = d.ReadString(m)
	case mKey:
		v.Key, err = d.ReadString(m)
	case mTags, mNames:
		var l []string
		l, err = readStrings(d, m)
		if m == mTags {
			v.Tags = l
		} else {
			v.Names = l
		}
	case mLimit:
		var x int32
		x, err = d.ReadInt32(m)
		v.Limit = &x
	case mWhen:
		var x time.Time
		x, err = d.ReadTimestamp(m)
		v.When = &x
	case mStamps:
		v.Stamps, err = readTimes(d, m)
	case mFlag:
		var x bool
		x, err = d.ReadBool(m)
		v.Flag = &x
	case mMeta:
		v.Meta, err = readMap(d, m)
	case mSum:
		var x string
		x, err = d.ReadString(m)
		v.Checksum = &x
	case mData:
		v.Data, err = d.ReadBlob(m)
	case mBody:
		v.Body, err = d.ReadBody(m)
	}
	return err
}

func readStrings(d smithy.Deserializer, m *smithy.Member) ([]string, error) {
	if err := d.ReadList(m); err != nil {
		return nil, err
	}
	out := []string{}
	for {
		more, err := d.ReadListElement()
		if err != nil || !more {
			return out, err
		}
		s, err := d.ReadString(m.Target.Members[0])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
}

func readTimes(d smithy.Deserializer, m *smithy.Member) ([]time.Time, error) {
	if err := d.ReadList(m); err != nil {
		return nil, err
	}
	out := []time.Time{}
	for {
		more, err := d.ReadListElement()
		if err != nil || !more {
			return out, err
		}
		t, err := d.ReadTimestamp(m.Target.Members[0])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
}

func readMap(d smithy.Deserializer, m *smithy.Member) (map[string]string, error) {
	if err := d.ReadMap(m); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for {
		k, ok, err := d.ReadMapKey()
		if err != nil || !ok {
			return out, err
		}
		s, err := d.ReadString(m.Target.Members[1])
		if err != nil {
			return nil, err
		}
		out[k] = s
	}
}

// Serialize writes the output members, as generated code does.
func (v *put) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, putOutput)
	if v.When != nil {
		s.WriteTimestamp(mWhen, *v.When)
	}
	if len(v.Stamps) > 0 {
		s.WriteList(mStamps, len(v.Stamps))
		for _, t := range v.Stamps {
			s.WriteTimestamp(timestampList.Members[0], t)
		}
		s.CloseList()
	}
	if len(v.Names) > 0 {
		s.WriteList(mNames, len(v.Names))
		for _, n := range v.Names {
			s.WriteString(stringList.Members[0], n)
		}
		s.CloseList()
	}
	if v.Flag != nil {
		s.WriteBool(mFlag, *v.Flag)
	}
	if len(v.Meta) > 0 {
		s.WriteMap(mMeta, len(v.Meta))
		for _, k := range slices.Sorted(maps.Keys(v.Meta)) {
			s.WriteMapKey(k)
			s.WriteString(stringMap.Members[1], v.Meta[k])
		}
		s.CloseMap()
	}
	s.WriteInt32(mStatus, v.Status)
	if v.Data != nil {
		s.WriteBlob(mData, v.Data)
	}
	s.CloseStruct()
}

// body is a request body with trailers that the edge would supply.
type body struct {
	data     []byte
	trailers map[string]string
}

func (b *body) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.data)), nil }

func (b *body) Len() (int64, bool) { return int64(len(b.data)), true }

func (b *body) Trailer(name string) (string, bool) {
	v, ok := b.trailers[http.CanonicalHeaderKey(name)]
	return v, ok
}
