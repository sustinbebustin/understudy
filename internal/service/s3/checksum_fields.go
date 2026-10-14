package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// checksumFields are the x-amz-checksum-* fields of one s3api input or
// output, indexed by algorithm as checksumMembers is. The constructors
// below are the only place the service names those fields, one per shape,
// so an algorithm added to checksum is mapped here and nowhere else.
type checksumFields [checksum.XXHASH128 + 1]**string

// members reads the fields.
func (f checksumFields) members() checksumMembers {
	var m checksumMembers
	for _, a := range checksum.Algorithms() {
		m[a] = *f[a]
	}
	return m
}

// set writes m to the fields, clearing those m leaves unset.
func (f checksumFields) set(m checksumMembers) {
	for _, a := range checksum.Algorithms() {
		*f[a] = m[a]
	}
}

func putObjectInputFields(v *s3api.PutObjectInput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func putObjectOutputFields(v *s3api.PutObjectOutput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func uploadPartInputFields(v *s3api.UploadPartInput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func uploadPartOutputFields(v *s3api.UploadPartOutput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func partFields(v *s3api.Part) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func completeMultipartUploadInputFields(v *s3api.CompleteMultipartUploadInput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func completeMultipartUploadOutputFields(v *s3api.CompleteMultipartUploadOutput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func completedPartFields(v *s3api.CompletedPart) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func headObjectOutputFields(v *s3api.HeadObjectOutput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func getObjectOutputFields(v *s3api.GetObjectOutput) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func copyObjectResultFields(v *s3api.CopyObjectResult) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func copyPartResultFields(v *s3api.CopyPartResult) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func checksumShapeFields(v *s3api.Checksum) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}

func objectPartFields(v *s3api.ObjectPart) checksumFields {
	return checksumFields{
		checksum.CRC32: &v.ChecksumCRC32, checksum.CRC32C: &v.ChecksumCRC32C, checksum.CRC64NVME: &v.ChecksumCRC64NVME,
		checksum.SHA1: &v.ChecksumSHA1, checksum.SHA256: &v.ChecksumSHA256, checksum.SHA512: &v.ChecksumSHA512, checksum.MD5: &v.ChecksumMD5,
		checksum.XXHASH64: &v.ChecksumXXHASH64, checksum.XXHASH3: &v.ChecksumXXHASH3, checksum.XXHASH128: &v.ChecksumXXHASH128,
	}
}
