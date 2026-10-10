package restjson_test

import (
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Hand-written stand-ins for generated Lambda shapes, following the
// generated Serialize and Deserialize pattern.

var (
	str      = &smithy.Schema{ID: "com.amazonaws.lambda#String", Type: smithy.TypeString}
	integer  = &smithy.Schema{ID: "com.amazonaws.lambda#Integer", Type: smithy.TypeInteger}
	long     = &smithy.Schema{ID: "com.amazonaws.lambda#Long", Type: smithy.TypeLong}
	double   = &smithy.Schema{ID: "com.amazonaws.lambda#Double", Type: smithy.TypeDouble}
	boolean  = &smithy.Schema{ID: "com.amazonaws.lambda#Boolean", Type: smithy.TypeBoolean}
	stamp    = &smithy.Schema{ID: "com.amazonaws.lambda#Timestamp", Type: smithy.TypeTimestamp}
	blob     = &smithy.Schema{ID: "com.amazonaws.lambda#Blob", Type: smithy.TypeBlob, Sensitive: true}
	strList  = &smithy.Schema{ID: "com.amazonaws.lambda#StringList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: str}}}
	strMap   = &smithy.Schema{ID: "com.amazonaws.lambda#Tags", Type: smithy.TypeMap, Members: []*smithy.Member{{Name: "key", Target: str}, {Name: "value", Index: 1, Target: str}}}
	codeA    = &smithy.Member{Name: "S3Key", Target: str}
	codeZip  = &smithy.Member{Name: "ZipFile", Index: 1, Target: blob}
	codeType = &smithy.Schema{ID: "com.amazonaws.lambda#FunctionCode", Type: smithy.TypeStructure, Members: []*smithy.Member{codeA, codeZip}}

	// Configuration: every body member kind.
	fName     = &smithy.Member{Name: "FunctionName", Target: str, JSONName: "name"}
	fTimeout  = &smithy.Member{Name: "Timeout", Index: 1, Target: integer}
	fSize     = &smithy.Member{Name: "CodeSize", Index: 2, Target: long}
	fRatio    = &smithy.Member{Name: "Ratio", Index: 3, Target: double}
	fEnabled  = &smithy.Member{Name: "Enabled", Index: 4, Target: boolean}
	fCreated  = &smithy.Member{Name: "Created", Index: 5, Target: stamp}
	fModified = &smithy.Member{Name: "LastModified", Index: 6, Target: stamp, TimestampFormat: smithy.DateTime}
	fZip      = &smithy.Member{Name: "Zip", Index: 7, Target: blob}
	fTags     = &smithy.Member{Name: "Tags", Index: 8, Target: strMap}
	fLayers   = &smithy.Member{Name: "Layers", Index: 9, Target: strList}
	fCode     = &smithy.Member{Name: "Code", Index: 10, Target: codeType}
	// fPath is bound to the URI, so a body member of the same name is
	// ignored.
	fPath = &smithy.Member{Name: "Path", Index: 11, Target: str, HTTP: smithy.HTTPLabel{}}

	configSchema = &smithy.Schema{
		ID: "com.amazonaws.lambda#Config", Type: smithy.TypeStructure,
		Members: []*smithy.Member{fName, fTimeout, fSize, fRatio, fEnabled, fCreated, fModified, fZip, fTags, fLayers, fCode, fPath},
	}

	// Invoke.
	iName      = &smithy.Member{Name: "FunctionName", Target: str, Required: true, HTTP: smithy.HTTPLabel{}}
	iType      = &smithy.Member{Name: "InvocationType", Index: 1, Target: str, HTTP: smithy.HTTPHeader{Name: "X-Amz-Invocation-Type"}}
	iQualifier = &smithy.Member{Name: "Qualifier", Index: 2, Target: str, HTTP: smithy.HTTPQuery{Name: "Qualifier"}}
	iPayload   = &smithy.Member{Name: "Payload", Index: 3, Target: blob, HTTP: smithy.HTTPPayload{}}
	oStatus    = &smithy.Member{Name: "StatusCode", Target: integer, HTTP: smithy.HTTPResponseCode{}}
	oError     = &smithy.Member{Name: "FunctionError", Index: 1, Target: str, HTTP: smithy.HTTPHeader{Name: "X-Amz-Function-Error"}}
	oLog       = &smithy.Member{Name: "LogResult", Index: 2, Target: str, HTTP: smithy.HTTPHeader{Name: "X-Amz-Log-Result"}}
	oPayload   = &smithy.Member{Name: "Payload", Index: 3, Target: blob, HTTP: smithy.HTTPPayload{}}
	oVersion   = &smithy.Member{Name: "ExecutedVersion", Index: 4, Target: str, HTTP: smithy.HTTPHeader{Name: "X-Amz-Executed-Version"}}

	invokeIn  = &smithy.Schema{ID: "com.amazonaws.lambda#InvocationRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{iName, iType, iQualifier, iPayload}}
	invokeOut = &smithy.Schema{ID: "com.amazonaws.lambda#InvocationResponse", Type: smithy.TypeStructure, Members: []*smithy.Member{oStatus, oError, oLog, oPayload, oVersion}}
	unit      = &smithy.Schema{ID: "smithy.api#Unit", Type: smithy.TypeUnit}
)

// lambda is a service descriptor with Lambda's malformedRequestError.
func lambda() *registry.Service {
	return &registry.Service{
		ID: "lambda", SigningName: "lambda", Protocols: []ref.ProtocolID{ref.RESTJSON1},
		MalformedRequestError: awserr.Identity{
			Shape: "com.amazonaws.lambda#InvalidRequestContentException", JSONType: "InvalidRequestContentException",
			QueryCode: "InvalidRequestContentException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		},
	}
}

func op(svc *registry.Service, name, method, uri string, in, out *smithy.Schema) *registry.Operation {
	b, err := registry.ParseHTTPBinding(method, uri)
	if err != nil {
		panic(err)
	}
	o := &registry.Operation{Service: svc, ID: ref.OperationID{Service: svc.ID, Name: name}, Kind: registry.Modeled, Input: in, Output: out, HTTP: &b}
	svc.Operations = append(svc.Operations, o)
	return o
}

type code struct {
	S3Key *string
	Zip   []byte
}

type config struct {
	Name     *string
	Timeout  *int32
	Size     *int64
	Ratio    *float64
	Enabled  *bool
	Created  *time.Time
	Modified *time.Time
	Zip      []byte
	Tags     map[string]string
	Layers   []string
	Code     *code
	Path     *string
}

func (v *config) Serialize(s smithy.Serializer) { serializeConfig(s, nil, v) }

func serializeConfig(s smithy.Serializer, m *smithy.Member, v *config) {
	s.WriteStruct(m, configSchema)
	if v.Name != nil {
		s.WriteString(fName, *v.Name)
	}
	if v.Timeout != nil {
		s.WriteInt32(fTimeout, *v.Timeout)
	}
	if v.Size != nil {
		s.WriteInt64(fSize, *v.Size)
	}
	if v.Ratio != nil {
		s.WriteFloat64(fRatio, *v.Ratio)
	}
	if v.Enabled != nil {
		s.WriteBool(fEnabled, *v.Enabled)
	}
	if v.Created != nil {
		s.WriteTimestamp(fCreated, *v.Created)
	}
	if v.Modified != nil {
		s.WriteTimestamp(fModified, *v.Modified)
	}
	if v.Zip != nil {
		s.WriteBlob(fZip, v.Zip)
	}
	if v.Tags != nil {
		s.WriteMap(fTags, len(v.Tags))
		for _, k := range slices.Sorted(maps.Keys(v.Tags)) {
			s.WriteMapKey(k)
			s.WriteString(strMap.Members[1], v.Tags[k])
		}
		s.CloseMap()
	}
	if v.Layers != nil {
		s.WriteList(fLayers, len(v.Layers))
		for _, l := range v.Layers {
			s.WriteString(strList.Members[0], l)
		}
		s.CloseList()
	}
	if v.Code != nil {
		s.WriteStruct(fCode, codeType)
		if v.Code.S3Key != nil {
			s.WriteString(codeA, *v.Code.S3Key)
		}
		if v.Code.Zip != nil {
			s.WriteBlob(codeZip, v.Code.Zip)
		}
		s.CloseStruct()
	}
	s.CloseStruct()
}

func (v *config) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(configSchema); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		if err := readConfigMember(d, m, v); err != nil {
			return err
		}
	}
}

func readConfigMember(d smithy.Deserializer, m *smithy.Member, v *config) (err error) {
	switch m {
	case fName:
		v.Name, err = ptr(d.ReadString(m))
	case fTimeout:
		v.Timeout, err = ptr(d.ReadInt32(m))
	case fSize:
		v.Size, err = ptr(d.ReadInt64(m))
	case fRatio:
		v.Ratio, err = ptr(d.ReadFloat64(m))
	case fEnabled:
		v.Enabled, err = ptr(d.ReadBool(m))
	case fCreated, fModified:
		var t *time.Time
		t, err = ptr(d.ReadTimestamp(m))
		if m == fCreated {
			v.Created = t
		} else {
			v.Modified = t
		}
	case fZip:
		v.Zip, err = d.ReadBlob(m)
	case fTags:
		v.Tags, err = readTags(d, m)
	case fLayers:
		v.Layers, err = readLayers(d, m)
	case fCode:
		v.Code, err = readCode(d)
	case fPath:
		v.Path, err = ptr(d.ReadString(m))
	}
	return err
}

func ptr[T any](v T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func readTags(d smithy.Deserializer, m *smithy.Member) (map[string]string, error) {
	if err := d.ReadMap(m); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for {
		k, ok, err := d.ReadMapKey()
		if err != nil || !ok {
			return out, err
		}
		if out[k], err = d.ReadString(strMap.Members[1]); err != nil {
			return nil, err
		}
	}
}

func readLayers(d smithy.Deserializer, m *smithy.Member) ([]string, error) {
	if err := d.ReadList(m); err != nil {
		return nil, err
	}
	out := []string{}
	for {
		more, err := d.ReadListElement()
		if err != nil || !more {
			return out, err
		}
		s, err := d.ReadString(strList.Members[0])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
}

func readCode(d smithy.Deserializer) (*code, error) {
	if err := d.ReadStruct(codeType); err != nil {
		return nil, err
	}
	c := &code{}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return c, err
		}
		switch m {
		case codeA:
			c.S3Key, err = ptr(d.ReadString(m))
		case codeZip:
			c.Zip, err = d.ReadBlob(m)
		}
		if err != nil {
			return nil, err
		}
	}
}

type invokeInput struct {
	FunctionName   string
	InvocationType *string
	Qualifier      *string
	Payload        []byte
}

func (v *invokeInput) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(invokeIn); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		switch m {
		case iName:
			v.FunctionName, err = d.ReadString(m)
		case iType:
			v.InvocationType, err = ptr(d.ReadString(m))
		case iQualifier:
			v.Qualifier, err = ptr(d.ReadString(m))
		case iPayload:
			v.Payload, err = d.ReadBlob(m)
		}
		if err != nil {
			return err
		}
	}
}

type invokeOutput struct {
	StatusCode      int32
	FunctionError   *string
	LogResult       *string
	Payload         []byte
	ExecutedVersion *string
}

func (v *invokeOutput) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, invokeOut)
	s.WriteInt32(oStatus, v.StatusCode)
	if v.FunctionError != nil {
		s.WriteString(oError, *v.FunctionError)
	}
	if v.LogResult != nil {
		s.WriteString(oLog, *v.LogResult)
	}
	if v.Payload != nil {
		s.WriteBlob(oPayload, v.Payload)
	}
	if v.ExecutedVersion != nil {
		s.WriteString(oVersion, *v.ExecutedVersion)
	}
	s.CloseStruct()
}

// empty is a Unit input or output.
type empty struct{}

func (empty) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(unit); err != nil {
		return err
	}
	_, _, err := d.ReadStructMember()
	return err
}

func (empty) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, unit)
	s.CloseStruct()
}

// edgeBody is the edge's view of a request body.
type edgeBody struct{ r *http.Request }

func (b edgeBody) Open() (io.ReadCloser, error) { return b.r.Body, nil }

func (b edgeBody) Len() (int64, bool) { return b.r.ContentLength, b.r.ContentLength >= 0 }

func (edgeBody) Trailer(string) (string, bool) { return "", false }
