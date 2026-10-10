package restxml_test

import (
	"io"
	"net/http"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Hand-written stand-ins for generated S3 shapes (S3 model at the project
// pin), following the generated Serialize and Deserialize pattern.

const s3Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

var (
	str     = &smithy.Schema{ID: "com.amazonaws.s3#String", Type: smithy.TypeString}
	integer = &smithy.Schema{ID: "com.amazonaws.s3#Integer", Type: smithy.TypeInteger}
	long    = &smithy.Schema{ID: "com.amazonaws.s3#Long", Type: smithy.TypeLong}
	boolean = &smithy.Schema{ID: "com.amazonaws.s3#Boolean", Type: smithy.TypeBoolean}
	stamp   = &smithy.Schema{ID: "com.amazonaws.s3#Timestamp", Type: smithy.TypeTimestamp}
	stream  = &smithy.Schema{ID: "com.amazonaws.s3#StreamingBlob", Type: smithy.TypeBlob, Streaming: true}

	// ListObjectsV2.
	objKey      = &smithy.Member{Name: "Key", Target: str}
	objModified = &smithy.Member{Name: "LastModified", Index: 1, Target: stamp}
	objETag     = &smithy.Member{Name: "ETag", Index: 2, Target: str}
	objSize     = &smithy.Member{Name: "Size", Index: 3, Target: long}
	objClass    = &smithy.Member{Name: "StorageClass", Index: 4, Target: str}
	object      = &smithy.Schema{ID: "com.amazonaws.s3#Object", Type: smithy.TypeStructure, Members: []*smithy.Member{objKey, objModified, objETag, objSize, objClass}}
	objectList  = &smithy.Schema{ID: "com.amazonaws.s3#ObjectList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: object}}}
	cpPrefix    = &smithy.Member{Name: "Prefix", Target: str}
	commonPfx   = &smithy.Schema{ID: "com.amazonaws.s3#CommonPrefix", Type: smithy.TypeStructure, Members: []*smithy.Member{cpPrefix}}
	commonList  = &smithy.Schema{ID: "com.amazonaws.s3#CommonPrefixList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: commonPfx}}}

	lBucket   = &smithy.Member{Name: "Bucket", Target: str, Required: true, HTTP: smithy.HTTPLabel{}}
	lDelim    = &smithy.Member{Name: "Delimiter", Index: 1, Target: str, HTTP: smithy.HTTPQuery{Name: "delimiter"}}
	lEncoding = &smithy.Member{Name: "EncodingType", Index: 2, Target: str, HTTP: smithy.HTTPQuery{Name: "encoding-type"}}
	lMaxKeys  = &smithy.Member{Name: "MaxKeys", Index: 3, Target: integer, HTTP: smithy.HTTPQuery{Name: "max-keys"}}
	lPrefix   = &smithy.Member{Name: "Prefix", Index: 4, Target: str, HTTP: smithy.HTTPQuery{Name: "prefix"}}
	listIn    = &smithy.Schema{ID: "com.amazonaws.s3#ListObjectsV2Request", Type: smithy.TypeStructure, Members: []*smithy.Member{lBucket, lDelim, lEncoding, lMaxKeys, lPrefix}}

	oTruncated = &smithy.Member{Name: "IsTruncated", Target: boolean}
	oContents  = &smithy.Member{Name: "Contents", Index: 1, Target: objectList, XMLFlattened: true}
	oName      = &smithy.Member{Name: "Name", Index: 2, Target: str}
	oPrefix    = &smithy.Member{Name: "Prefix", Index: 3, Target: str}
	oDelim     = &smithy.Member{Name: "Delimiter", Index: 4, Target: str}
	oMaxKeys   = &smithy.Member{Name: "MaxKeys", Index: 5, Target: integer}
	oCommon    = &smithy.Member{Name: "CommonPrefixes", Index: 6, Target: commonList, XMLFlattened: true}
	oEncoding  = &smithy.Member{Name: "EncodingType", Index: 7, Target: str}
	oKeyCount  = &smithy.Member{Name: "KeyCount", Index: 8, Target: integer}
	oCharged   = &smithy.Member{Name: "RequestCharged", Index: 9, Target: str, HTTP: smithy.HTTPHeader{Name: "x-amz-request-charged"}}
	listOut    = &smithy.Schema{
		ID: "com.amazonaws.s3#ListObjectsV2Output", Type: smithy.TypeStructure, XMLName: "ListBucketResult",
		Members: []*smithy.Member{oTruncated, oContents, oName, oPrefix, oDelim, oMaxKeys, oCommon, oEncoding, oKeyCount, oCharged},
	}

	// GetBucketLocation.
	locIn  = &smithy.Schema{ID: "com.amazonaws.s3#GetBucketLocationRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{lBucket}}
	locMem = &smithy.Member{Name: "LocationConstraint", Target: str}
	locOut = &smithy.Schema{ID: "com.amazonaws.s3#GetBucketLocationOutput", Type: smithy.TypeStructure, XMLName: "LocationConstraint", Members: []*smithy.Member{locMem}}

	// GetBucketAcl: a Grantee carries xsi:type.
	granteeID   = &smithy.Member{Name: "ID", Target: str}
	granteeType = &smithy.Member{Name: "Type", Index: 1, Target: str, XMLAttribute: true, XMLName: "xsi:type"}
	grantee     = &smithy.Schema{ID: "com.amazonaws.s3#Grantee", Type: smithy.TypeStructure, Members: []*smithy.Member{granteeID, granteeType}}
	grantG      = &smithy.Member{Name: "Grantee", Target: grantee, XMLNamespace: &smithy.XMLNamespace{URI: xmlw.XSINamespace, Prefix: "xsi"}}
	grantPerm   = &smithy.Member{Name: "Permission", Index: 1, Target: str}
	grant       = &smithy.Schema{ID: "com.amazonaws.s3#Grant", Type: smithy.TypeStructure, Members: []*smithy.Member{grantG, grantPerm}}
	grants      = &smithy.Schema{ID: "com.amazonaws.s3#Grants", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: grant, XMLName: "Grant"}}}
	aclGrants   = &smithy.Member{Name: "Grants", Target: grants, XMLName: "AccessControlList"}
	aclOut      = &smithy.Schema{ID: "com.amazonaws.s3#GetBucketAclOutput", Type: smithy.TypeStructure, XMLName: "AccessControlPolicy", Members: []*smithy.Member{aclGrants}}

	// CompleteMultipartUpload.
	partETag   = &smithy.Member{Name: "ETag", Target: str}
	partNumber = &smithy.Member{Name: "PartNumber", Index: 1, Target: integer}
	part       = &smithy.Schema{ID: "com.amazonaws.s3#CompletedPart", Type: smithy.TypeStructure, Members: []*smithy.Member{partETag, partNumber}}
	partList   = &smithy.Schema{ID: "com.amazonaws.s3#CompletedPartList", Type: smithy.TypeList, Members: []*smithy.Member{{Name: "member", Target: part}}}
	mpParts    = &smithy.Member{Name: "Parts", Target: partList, XMLName: "Part", XMLFlattened: true}
	mpUpload   = &smithy.Schema{ID: "com.amazonaws.s3#CompletedMultipartUpload", Type: smithy.TypeStructure, Members: []*smithy.Member{mpParts}}
	cKey       = &smithy.Member{Name: "Key", Index: 1, Target: str, Required: true, HTTP: smithy.HTTPLabel{}}
	cUpload    = &smithy.Member{Name: "MultipartUpload", Index: 2, Target: mpUpload, XMLName: "CompleteMultipartUpload", HTTP: smithy.HTTPPayload{}}
	cUploadID  = &smithy.Member{Name: "UploadId", Index: 3, Target: str, Required: true, HTTP: smithy.HTTPQuery{Name: "uploadId"}}
	cSize      = &smithy.Member{Name: "MpuObjectSize", Index: 4, Target: long, HTTP: smithy.HTTPHeader{Name: "x-amz-mp-object-size"}}
	completeIn = &smithy.Schema{ID: "com.amazonaws.s3#CompleteMultipartUploadRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{lBucket, cKey, cUpload, cUploadID, cSize}}

	// PutObject.
	pBody = &smithy.Member{Name: "Body", Index: 2, Target: stream, HTTP: smithy.HTTPPayload{}}
	putIn = &smithy.Schema{ID: "com.amazonaws.s3#PutObjectRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{lBucket, cKey, pBody}}
)

func s3() *registry.Service {
	return &registry.Service{
		ID: "s3", SigningName: "s3", Protocols: []ref.ProtocolID{ref.RESTXML}, XMLNamespace: s3Namespace,
		MalformedRequestError: awserr.Identity{Shape: "com.amazonaws.s3#MalformedXML", QueryCode: "MalformedXML", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageUpper},
	}
}

func invalidArgument() awserr.Identity {
	return awserr.Identity{Shape: "com.amazonaws.s3#InvalidArgument", QueryCode: "InvalidArgument", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageUpper}
}

func s3InternalError() awserr.Identity {
	return awserr.Identity{Shape: "com.amazonaws.s3#InternalError", QueryCode: "InternalError", Status: 500, Fault: awserr.Receiver, MessageKey: awserr.MessageUpper}
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

type s3Object struct {
	Key          string
	LastModified time.Time
	ETag         string
	Size         int64
	StorageClass string
}

type listOutput struct {
	IsTruncated    bool
	Contents       []s3Object
	Name, Prefix   string
	Delimiter      *string
	MaxKeys        int32
	CommonPrefixes []string
	EncodingType   *string
	KeyCount       int32
}

func (v *listOutput) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, listOut)
	s.WriteBool(oTruncated, v.IsTruncated)
	if len(v.Contents) > 0 {
		s.WriteList(oContents, len(v.Contents))
		for _, o := range v.Contents {
			s.WriteStruct(objectList.Members[0], object)
			s.WriteString(objKey, o.Key)
			s.WriteTimestamp(objModified, o.LastModified)
			s.WriteString(objETag, o.ETag)
			s.WriteInt64(objSize, o.Size)
			s.WriteString(objClass, o.StorageClass)
			s.CloseStruct()
		}
		s.CloseList()
	}
	s.WriteString(oName, v.Name)
	s.WriteString(oPrefix, v.Prefix)
	if v.Delimiter != nil {
		s.WriteString(oDelim, *v.Delimiter)
	}
	s.WriteInt32(oMaxKeys, v.MaxKeys)
	if len(v.CommonPrefixes) > 0 {
		s.WriteList(oCommon, len(v.CommonPrefixes))
		for _, p := range v.CommonPrefixes {
			s.WriteStruct(commonList.Members[0], commonPfx)
			s.WriteString(cpPrefix, p)
			s.CloseStruct()
		}
		s.CloseList()
	}
	if v.EncodingType != nil {
		s.WriteString(oEncoding, *v.EncodingType)
	}
	s.WriteInt32(oKeyCount, v.KeyCount)
	s.CloseStruct()
}

type listInput struct {
	Bucket                          string
	Delimiter, EncodingType, Prefix *string
	MaxKeys                         *int32
}

func (v *listInput) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(listIn); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		switch m {
		case lBucket:
			v.Bucket, err = d.ReadString(m)
		case lDelim:
			v.Delimiter, err = ptr(d.ReadString(m))
		case lEncoding:
			v.EncodingType, err = ptr(d.ReadString(m))
		case lPrefix:
			v.Prefix, err = ptr(d.ReadString(m))
		case lMaxKeys:
			v.MaxKeys, err = ptr(d.ReadInt32(m))
		}
		if err != nil {
			return err
		}
	}
}

type locationOutput struct{ LocationConstraint *string }

func (v *locationOutput) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, locOut)
	if v.LocationConstraint != nil {
		s.WriteString(locMem, *v.LocationConstraint)
	}
	s.CloseStruct()
}

type aclGrant struct{ ID, Type, Permission string }

type aclOutput struct{ Grants []aclGrant }

func (v *aclOutput) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, aclOut)
	s.WriteList(aclGrants, len(v.Grants))
	for _, g := range v.Grants {
		s.WriteStruct(grants.Members[0], grant)
		s.WriteStruct(grantG, grantee)
		s.WriteString(granteeID, g.ID)
		s.WriteString(granteeType, g.Type)
		s.CloseStruct()
		s.WriteString(grantPerm, g.Permission)
		s.CloseStruct()
	}
	s.CloseList()
	s.CloseStruct()
}

type completedPart struct {
	ETag       *string
	PartNumber *int32
}

// complete is CompleteMultipartUpload's input, and doubles as an output
// with the same payload for the encode-then-decode property.
type complete struct {
	Bucket, Key, UploadID string
	Size                  *int64
	Parts                 []completedPart
	hasUpload             bool
}

func (v *complete) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(completeIn); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		switch m {
		case lBucket:
			v.Bucket, err = d.ReadString(m)
		case cKey:
			v.Key, err = d.ReadString(m)
		case cUploadID:
			v.UploadID, err = d.ReadString(m)
		case cSize:
			v.Size, err = ptr(d.ReadInt64(m))
		case cUpload:
			v.hasUpload = true
			v.Parts, err = readUpload(d)
		}
		if err != nil {
			return err
		}
	}
}

func readUpload(d smithy.Deserializer) ([]completedPart, error) {
	if err := d.ReadStruct(mpUpload); err != nil {
		return nil, err
	}
	var parts []completedPart
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return parts, err
		}
		if m != mpParts {
			continue
		}
		if err := d.ReadList(m); err != nil {
			return nil, err
		}
		for {
			more, err := d.ReadListElement()
			if err != nil {
				return nil, err
			}
			if !more {
				break
			}
			p, err := readPart(d)
			if err != nil {
				return nil, err
			}
			parts = append(parts, p)
		}
	}
}

func readPart(d smithy.Deserializer) (completedPart, error) {
	var p completedPart
	if err := d.ReadStruct(part); err != nil {
		return p, err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return p, err
		}
		switch m {
		case partETag:
			p.ETag, err = ptr(d.ReadString(m))
		case partNumber:
			p.PartNumber, err = ptr(d.ReadInt32(m))
		}
		if err != nil {
			return p, err
		}
	}
}

func (v *complete) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, completeIn)
	s.WriteStruct(cUpload, mpUpload)
	if len(v.Parts) > 0 {
		s.WriteList(mpParts, len(v.Parts))
		for _, p := range v.Parts {
			s.WriteStruct(partList.Members[0], part)
			if p.ETag != nil {
				s.WriteString(partETag, *p.ETag)
			}
			if p.PartNumber != nil {
				s.WriteInt32(partNumber, *p.PartNumber)
			}
			s.CloseStruct()
		}
		s.CloseList()
	}
	s.CloseStruct()
	s.CloseStruct()
}

type putInput struct {
	Bucket, Key string
	Body        smithy.Body
}

func (v *putInput) Deserialize(d smithy.Deserializer) error {
	if err := d.ReadStruct(putIn); err != nil {
		return err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return err
		}
		switch m {
		case lBucket:
			v.Bucket, err = d.ReadString(m)
		case cKey:
			v.Key, err = d.ReadString(m)
		case pBody:
			v.Body, err = d.ReadBody(m)
		}
		if err != nil {
			return err
		}
	}
}

func ptr[T any](v T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// edgeBody is the edge's view of a request body.
type edgeBody struct{ r *http.Request }

func (b edgeBody) Open() (io.ReadCloser, error) { return b.r.Body, nil }

func (b edgeBody) Len() (int64, bool) { return b.r.ContentLength, b.r.ContentLength >= 0 }

func (edgeBody) Trailer(string) (string, bool) { return "", false }
