#!/usr/bin/env bash
# S3: the aws s3 commands (mb, cp, sync, ls, mv, rm, rb, presign) and s3api calls
# for buckets, subresources, objects, listings, versioning, multipart, tagging,
# checksums, conditional requests, SSE-KMS, addressing styles and notifications.
# Arbiter: docs/research/phase1/s3.md.

# mkbucket [create-bucket args...]: create a bucket and set BUCKET.
mkbucket() {
  BUCKET=$(new_name cli-s3)
  aws_ok s3api create-bucket --bucket "$BUCKET" "$@"
}

# put_obj KEY BODY [put-object args...]: write a small object (no output kept).
put_obj() {
  local key=$1 body=$2
  shift 2
  local f
  f=$(mktemp "$UE_TMP/body.XXXXXX")
  printf '%s' "$body" >"$f"
  aws_ok s3api put-object --bucket "$BUCKET" --key "$key" --body "$f" "$@"
}

md5_hex() { printf '%s' "$1" | md5sum | cut -d' ' -f1; }

# empty_file: print the path of an empty file (--body rejects /dev/null).
empty_file() {
  local f="$UE_TMP/empty"
  : >"$f"
  printf '%s' "$f"
}

# checksum_b64 ALGORITHM FILE: base64 of the raw checksum of FILE.
# CRC32C and CRC64NVME are computed bitwise (reflected); the helper self-checks
# against the published check values of "123456789" so a bug here cannot pass
# silently as an emulator failure.
checksum_b64() {
  python3 -I - "$1" "$2" <<'PY'
import base64, hashlib, struct, sys, zlib

alg, path = sys.argv[1:3]

def reflected_crc(data, poly, width, init, xorout):
    mask = (1 << width) - 1
    crc = init
    for b in data:
        crc ^= b
        for _ in range(8):
            crc = (crc >> 1) ^ poly if crc & 1 else crc >> 1
    return (crc ^ xorout) & mask

def crc32c(d):
    return reflected_crc(d, 0x82F63B78, 32, 0xFFFFFFFF, 0xFFFFFFFF)

def crc64nvme(d):
    return reflected_crc(d, 0x9A6C9329AC4BC9B5, 64, 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF)

assert crc32c(b"123456789") == 0xE3069283, "crc32c self-check"
assert crc64nvme(b"123456789") == 0xAE8B14860A799888, "crc64nvme self-check"

data = open(path, "rb").read()
if alg == "CRC32":
    raw = struct.pack(">I", zlib.crc32(data) & 0xFFFFFFFF)
elif alg == "CRC32C":
    raw = struct.pack(">I", crc32c(data))
elif alg == "CRC64NVME":
    raw = struct.pack(">Q", crc64nvme(data))
elif alg == "SHA1":
    raw = hashlib.sha1(data).digest()
elif alg == "SHA256":
    raw = hashlib.sha256(data).digest()
else:
    sys.exit("unknown algorithm " + alg)
print(base64.b64encode(raw).decode())
PY
}

# mpu_etag FILE PARTSIZE: the multipart ETag (without quotes) of FILE uploaded in
# PARTSIZE-byte parts: md5 of the concatenated part md5s, "-" part count.
mpu_etag() {
  python3 -I - "$1" "$2" <<'PY'
import hashlib, sys
path, size = sys.argv[1], int(sys.argv[2])
digests = []
with open(path, "rb") as f:
    while chunk := f.read(size):
        digests.append(hashlib.md5(chunk).digest())
print(hashlib.md5(b"".join(digests)).hexdigest() + "-" + str(len(digests)))
PY
}

# make_file PATH BYTES: a file of random bytes.
make_file() { head -c "$2" /dev/urandom >"$1"; }

# aws s3: mb, rb, cp, ls, mv, rm, sync, presign

test_s3_mb_and_rb() {
  local b
  b=$(new_name cli-mb)
  aws_ok s3 mb "s3://$b"
  assert_contains "make_bucket: $b" "$AWS_OUT" 'mb output'
  aws_ok s3api head-bucket --bucket "$b"
  aws_ok s3 rb "s3://$b"
  assert_contains "remove_bucket: $b" "$AWS_OUT" 'rb output'
  aws_error 404 404 s3api head-bucket --bucket "$b"
}

test_s3_ls_lists_buckets() {
  mkbucket
  aws_ok s3 ls
  assert_contains " $BUCKET" "$AWS_OUT" 'bucket list'
}

test_s3_cp_upload_and_download_round_trip() {
  mkbucket
  local src="$UE_TMP/rt-src.txt" dst="$UE_TMP/rt-dst.txt"
  printf 'round trip body\n' >"$src"
  aws_ok s3 cp "$src" "s3://$BUCKET/dir/rt.txt"
  assert_contains "upload:" "$AWS_OUT" 'cp output'
  aws_ok s3 cp "s3://$BUCKET/dir/rt.txt" "$dst"
  assert_contains "download:" "$AWS_OUT" 'cp output'
  cmp "$src" "$dst" || fail 'downloaded bytes differ from uploaded bytes'
}

test_s3_cp_stdin_and_stdout() {
  mkbucket
  printf 'from stdin' | aws s3 cp - "s3://$BUCKET/stdin.txt" >/dev/null
  aws_ok s3 cp "s3://$BUCKET/stdin.txt" -
  assert_eq 'from stdin' "$AWS_OUT" 'object body on stdout'
}

test_s3_cp_multipart_upload_of_large_file() {
  mkbucket
  local src="$UE_TMP/big.bin" dst="$UE_TMP/big.out"
  make_file "$src" 20971520
  aws_ok s3 cp "$src" "s3://$BUCKET/big.bin"
  aws_ok s3api head-object --bucket "$BUCKET" --key big.bin
  assert_jq '.ContentLength' '20971520'
  assert_jq '.ETag' "\"$(mpu_etag "$src" 8388608)\""
  aws_ok s3 cp "s3://$BUCKET/big.bin" "$dst"
  cmp "$src" "$dst" || fail 'multipart round trip changed the bytes'
}

test_s3_cp_guesses_content_type() {
  mkbucket
  local src="$UE_TMP/page.html"
  printf '<p>hi</p>' >"$src"
  aws_ok s3 cp "$src" "s3://$BUCKET/page.html"
  aws_ok s3api head-object --bucket "$BUCKET" --key page.html
  assert_jq '.ContentType' 'text/html'
}

test_s3_cp_with_metadata_and_content_headers() {
  mkbucket
  local src="$UE_TMP/hdr.txt"
  printf 'x' >"$src"
  aws_ok s3 cp "$src" "s3://$BUCKET/hdr.txt" --metadata Owner=Team,Stage=Test \
    --cache-control max-age=60 --content-disposition 'attachment; filename=hdr.txt' --content-language en
  aws_ok s3api head-object --bucket "$BUCKET" --key hdr.txt
  assert_jq_true '(.Metadata | with_entries(.key |= ascii_downcase)) == {"owner":"Team","stage":"Test"}' # key case: test_put_object_user_metadata_is_lowercased
  assert_jq '.CacheControl' 'max-age=60'
  assert_jq '.ContentDisposition' 'attachment; filename=hdr.txt'
  assert_jq '.ContentLanguage' 'en'
}

test_s3_cp_object_to_object() {
  mkbucket
  put_obj src.txt 'copy me'
  aws_ok s3 cp "s3://$BUCKET/src.txt" "s3://$BUCKET/dst.txt"
  aws_ok s3 cp "s3://$BUCKET/dst.txt" -
  assert_eq 'copy me' "$AWS_OUT"
}

test_s3_ls_shows_prefixes_and_objects() {
  mkbucket
  put_obj top.txt abcde
  put_obj dir/inner.txt fg
  aws_ok s3 ls "s3://$BUCKET/"
  assert_match 'PRE dir/' "$AWS_OUT" 'common prefix line'
  assert_match ' 5 top\.txt' "$AWS_OUT" 'object line'
  aws_ok s3 ls "s3://$BUCKET/dir/"
  assert_match ' 2 inner\.txt' "$AWS_OUT" 'object under prefix'
}

test_s3_ls_recursive_summarize() {
  mkbucket
  put_obj a.txt 11
  put_obj d/b.txt 2222
  aws_ok s3 ls "s3://$BUCKET/" --recursive --summarize
  assert_contains 'd/b.txt' "$AWS_OUT"
  assert_contains 'Total Objects: 2' "$AWS_OUT"
  assert_contains 'Total Size: 6' "$AWS_OUT"
}

test_s3_ls_missing_bucket() {
  aws_error NoSuchBucket 404 s3 ls "s3://$(new_name cli-nobucket)/"
  aws_error_message_contains 'The specified bucket does not exist'
}

test_s3_mv_object() {
  mkbucket
  put_obj old.txt moved
  aws_ok s3 mv "s3://$BUCKET/old.txt" "s3://$BUCKET/new.txt"
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key old.txt
  aws_ok s3 cp "s3://$BUCKET/new.txt" -
  assert_eq moved "$AWS_OUT"
}

test_s3_rm_object() {
  mkbucket
  put_obj gone.txt x
  aws_ok s3 rm "s3://$BUCKET/gone.txt"
  assert_contains 'delete:' "$AWS_OUT"
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key gone.txt
}

test_s3_rm_recursive() {
  mkbucket
  local i
  for i in 1 2 3; do put_obj "tree/k$i" "v$i"; done
  put_obj keep.txt keep
  aws_ok s3 rm "s3://$BUCKET/tree/" --recursive
  aws_ok s3api list-objects-v2 --bucket "$BUCKET"
  assert_jq_true '[.Contents[].Key] == ["keep.txt"]'
}

test_s3_rb_non_empty_bucket() {
  mkbucket
  put_obj k v
  aws_error BucketNotEmpty 409 s3 rb "s3://$BUCKET"
  aws_error_message_contains 'The bucket you tried to delete is not empty'
}

test_s3_rb_force_empties_and_removes_bucket() {
  mkbucket
  put_obj a 1
  put_obj b/c 2
  aws_ok s3 rb "s3://$BUCKET" --force
  aws_error 404 404 s3api head-bucket --bucket "$BUCKET"
}

test_s3_sync_local_to_bucket_and_back() {
  mkbucket
  local src="$UE_TMP/sync-src" back="$UE_TMP/sync-back"
  mkdir -p "$src/sub"
  printf 'alpha' >"$src/a.txt"
  printf 'bravo' >"$src/sub/b.txt"
  aws_ok s3 sync "$src" "s3://$BUCKET/data/"
  assert_contains 'upload:' "$AWS_OUT"
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --prefix data/
  assert_jq_true '[.Contents[].Key] | sort == ["data/a.txt","data/sub/b.txt"]'
  aws_ok s3 sync "s3://$BUCKET/data/" "$back"
  assert_eq alpha "$(cat "$back/a.txt")"
  assert_eq bravo "$(cat "$back/sub/b.txt")"
}

test_s3_sync_is_idempotent() {
  mkbucket
  local src="$UE_TMP/sync-idem"
  mkdir -p "$src"
  printf 'one' >"$src/one.txt"
  # sync skips a file when the object is at least as new as the local copy and
  # the sizes match; date the file in the past so the upload cannot be older.
  touch -d '1 hour ago' "$src/one.txt"
  aws_ok s3 sync "$src" "s3://$BUCKET/"
  aws_ok s3 sync "$src" "s3://$BUCKET/"
  assert_eq '' "$AWS_OUT" 'second sync transfers nothing'
}

test_s3_sync_uploads_only_changed_files() {
  mkbucket
  local src="$UE_TMP/sync-chg"
  mkdir -p "$src"
  printf 'one' >"$src/one.txt"
  printf 'two' >"$src/two.txt"
  touch -d '1 hour ago' "$src/one.txt" "$src/two.txt"
  aws_ok s3 sync "$src" "s3://$BUCKET/"
  printf 'two changed' >"$src/two.txt"
  touch -d '1 hour ago' "$src/two.txt"
  aws_ok s3 sync "$src" "s3://$BUCKET/"
  assert_contains 'two.txt' "$AWS_OUT"
  assert_not_contains 'one.txt' "$AWS_OUT"
}

test_s3_sync_delete_removes_remote_extras() {
  mkbucket
  local src="$UE_TMP/sync-del"
  mkdir -p "$src"
  printf 'keep' >"$src/keep.txt"
  put_obj extra.txt stale
  aws_ok s3 sync "$src" "s3://$BUCKET/" --delete
  assert_contains 'delete:' "$AWS_OUT"
  aws_ok s3api list-objects-v2 --bucket "$BUCKET"
  assert_jq_true '[.Contents[].Key] == ["keep.txt"]'
}

test_s3_sync_exclude_pattern() {
  mkbucket
  local src="$UE_TMP/sync-exc"
  mkdir -p "$src"
  printf 'a' >"$src/a.txt"
  printf 'b' >"$src/b.log"
  aws_ok s3 sync "$src" "s3://$BUCKET/" --exclude '*.log'
  aws_ok s3api list-objects-v2 --bucket "$BUCKET"
  assert_jq_true '[.Contents[].Key] == ["a.txt"]'
}

test_s3_presign_url_serves_object() {
  mkbucket
  put_obj shared.txt 'presigned body'
  aws_ok s3 presign "s3://$BUCKET/shared.txt" --expires-in 120
  local url=$AWS_OUT
  assert_match 'X-Amz-Signature=' "$url" 'presigned URL'
  assert_match 'X-Amz-Expires=120' "$url" 'presigned URL'
  local body
  body=$(curl -fsS "$url")
  assert_eq 'presigned body' "$body"
}

# Buckets

test_create_bucket_in_us_east_1() {
  local b
  b=$(new_name cli-cb)
  aws_ok s3api create-bucket --bucket "$b"
  assert_jq "(.Location // \"\") | tostring | startswith(\"/\") or startswith(\"http\")" 'true'
  aws_ok s3api get-bucket-location --bucket "$b"
  assert_jq '.LocationConstraint // "null"' 'null' 'us-east-1 buckets report an empty LocationConstraint'
}

test_create_bucket_in_other_region_needs_matching_location_constraint() {
  local b
  b=$(new_name cli-cbw)
  aws_ok s3api create-bucket --bucket "$b" --region us-west-2 --create-bucket-configuration LocationConstraint=us-west-2
  aws_ok s3api get-bucket-location --bucket "$b" --region us-west-2
  assert_jq '.LocationConstraint' 'us-west-2'
}

test_create_bucket_other_region_without_constraint() {
  aws_error IllegalLocationConstraintException 400 s3api create-bucket --bucket "$(new_name cli-cbnc)" --region us-west-2
  aws_error_message_contains 'The unspecified location constraint is incompatible for the region specific endpoint this request was sent to.'
}

test_create_bucket_us_east_1_rejects_us_east_1_constraint() {
  aws_error InvalidLocationConstraint 400 s3api create-bucket --bucket "$(new_name cli-cbue1)" \
    --create-bucket-configuration LocationConstraint=us-east-1
}

test_recreate_owned_bucket_in_us_east_1_is_ok() {
  mkbucket
  aws_ok s3api create-bucket --bucket "$BUCKET"
}

test_recreate_owned_bucket_outside_us_east_1_is_conflict() {
  local b
  b=$(new_name cli-cbdup)
  aws_ok s3api create-bucket --bucket "$b" --region us-west-2 --create-bucket-configuration LocationConstraint=us-west-2
  aws_error BucketAlreadyOwnedByYou 409 s3api create-bucket --bucket "$b" --region us-west-2 \
    --create-bucket-configuration LocationConstraint=us-west-2
  aws_error_message_contains 'Your previous request to create the named bucket succeeded and you already own it.'
}

test_create_bucket_invalid_name() {
  aws_error InvalidBucketName 400 s3api create-bucket --bucket ab
  aws_error_message_contains 'The specified bucket is not valid.'
}

test_head_bucket_missing() {
  aws_error 404 404 s3api head-bucket --bucket "$(new_name cli-nohead)"
}

test_head_bucket_reports_region() {
  mkbucket
  aws_run s3api head-bucket --bucket "$BUCKET"
  [ "$AWS_RC" -eq 0 ] || fail 'head-bucket failed'
  assert_eq us-east-1 "$(aws_response_header x-amz-bucket-region)" 'x-amz-bucket-region'
}

test_list_buckets_contains_bucket_with_owner() {
  mkbucket
  aws_ok s3api list-buckets
  assert_jq_true "[.Buckets[].Name] | index(\"$BUCKET\") != null"
  assert_jq_match '.Owner.ID' '^.+$'
  assert_jq_true ".Buckets | map(select(.Name == \"$BUCKET\"))[0].CreationDate | length > 0"
}

test_delete_missing_bucket() {
  aws_error NoSuchBucket 404 s3api delete-bucket --bucket "$(new_name cli-nodel)"
}

test_delete_bucket_non_empty() {
  mkbucket
  put_obj k v
  aws_error BucketNotEmpty 409 s3api delete-bucket --bucket "$BUCKET"
}

# Bucket subresources: fresh state and "not configured" errors (s3.md 4.1)

test_subresource_policy_not_configured() {
  mkbucket
  aws_error NoSuchBucketPolicy 404 s3api get-bucket-policy --bucket "$BUCKET"
  aws_error_message_contains 'The bucket policy does not exist'
}

test_subresource_tagging_not_configured() {
  mkbucket
  aws_error NoSuchTagSet 404 s3api get-bucket-tagging --bucket "$BUCKET"
  aws_error_message_contains 'The TagSet does not exist'
}

test_subresource_cors_not_configured() {
  mkbucket
  aws_error NoSuchCORSConfiguration 404 s3api get-bucket-cors --bucket "$BUCKET"
  aws_error_message_contains 'The CORS configuration does not exist'
}

test_subresource_lifecycle_not_configured() {
  mkbucket
  aws_error NoSuchLifecycleConfiguration 404 s3api get-bucket-lifecycle-configuration --bucket "$BUCKET"
  aws_error_message_contains 'The lifecycle configuration does not exist'
}

test_subresource_website_not_configured() {
  mkbucket
  aws_error NoSuchWebsiteConfiguration 404 s3api get-bucket-website --bucket "$BUCKET"
  aws_error_message_contains 'The specified bucket does not have a website configuration'
}

test_subresource_replication_not_configured() {
  mkbucket
  aws_error ReplicationConfigurationNotFoundError 404 s3api get-bucket-replication --bucket "$BUCKET"
  aws_error_message_contains 'The replication configuration was not found'
}

test_subresource_object_lock_not_configured() {
  mkbucket
  aws_error ObjectLockConfigurationNotFoundError 404 s3api get-object-lock-configuration --bucket "$BUCKET"
  aws_error_message_contains 'Object Lock configuration does not exist for this bucket'
}

test_subresource_default_ownership_controls() {
  mkbucket
  aws_ok s3api get-bucket-ownership-controls --bucket "$BUCKET"
  assert_jq_true '.OwnershipControls.Rules == [{"ObjectOwnership":"BucketOwnerEnforced"}]'
}

test_subresource_ownership_controls_not_found_after_delete() {
  mkbucket
  aws_ok s3api delete-bucket-ownership-controls --bucket "$BUCKET"
  aws_error OwnershipControlsNotFoundError 404 s3api get-bucket-ownership-controls --bucket "$BUCKET"
  aws_error_message_contains 'The bucket ownership controls were not found'
}

test_subresource_default_public_access_block() {
  mkbucket
  aws_ok s3api get-public-access-block --bucket "$BUCKET"
  assert_jq_true '.PublicAccessBlockConfiguration == {"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}'
}

test_subresource_public_access_block_not_found_after_delete() {
  mkbucket
  aws_ok s3api delete-public-access-block --bucket "$BUCKET"
  aws_error NoSuchPublicAccessBlockConfiguration 404 s3api get-public-access-block --bucket "$BUCKET"
  aws_error_message_contains 'The public access block configuration was not found'
}

test_subresource_default_encryption_is_sse_s3() {
  mkbucket
  aws_ok s3api get-bucket-encryption --bucket "$BUCKET"
  assert_jq '.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm' 'AES256'
}

test_subresource_default_versioning_is_empty() {
  mkbucket
  aws_ok s3api get-bucket-versioning --bucket "$BUCKET"
  assert_jq '.Status // "unset"' 'unset'
}

test_subresource_default_acl_is_owner_full_control() {
  mkbucket
  aws_ok s3api get-bucket-acl --bucket "$BUCKET"
  assert_jq_match '.Owner.ID' '^.+$'
  assert_jq '.Grants | length' '1'
  assert_jq '.Grants[0].Permission' 'FULL_CONTROL'
  assert_jq '.Grants[0].Grantee.Type' 'CanonicalUser'
}

test_subresource_default_logging_accelerate_request_payment_notification() {
  mkbucket
  aws_ok s3api get-bucket-logging --bucket "$BUCKET"
  assert_jq 'has("LoggingEnabled")' 'false'
  aws_ok s3api get-bucket-accelerate-configuration --bucket "$BUCKET"
  assert_jq '.Status // "unset"' 'unset'
  aws_ok s3api get-bucket-request-payment --bucket "$BUCKET"
  assert_jq '.Payer' 'BucketOwner'
  aws_ok s3api get-bucket-notification-configuration --bucket "$BUCKET"
  assert_jq_true 'keys == [] or (keys - ["TopicConfigurations","QueueConfigurations","LambdaFunctionConfigurations"] == [] and ([.[]] | all(length == 0)))'
}

test_subresource_metrics_configuration_not_found() {
  mkbucket
  aws_error NoSuchConfiguration 404 s3api get-bucket-metrics-configuration --bucket "$BUCKET" --id missing
}

test_subresource_missing_bucket_is_no_such_bucket() {
  local b
  b=$(new_name cli-nosub)
  aws_error NoSuchBucket 404 s3api get-bucket-policy --bucket "$b"
  aws_error NoSuchBucket 404 s3api get-bucket-cors --bucket "$b"
  aws_error NoSuchBucket 404 s3api get-bucket-versioning --bucket "$b"
}

test_bucket_policy_round_trip_and_delete() {
  mkbucket
  local policy
  policy=$(jq -nc --arg b "$BUCKET" '{Version:"2012-10-17",Statement:[{Sid:"Read",Effect:"Allow",Principal:"*",Action:"s3:GetObject",Resource:("arn:aws:s3:::" + $b + "/*")}]}')
  aws_ok s3api put-bucket-policy --bucket "$BUCKET" --policy "$policy"
  aws_ok s3api get-bucket-policy --bucket "$BUCKET"
  assert_jq_true ".Policy | fromjson == $policy"
  aws_ok s3api delete-bucket-policy --bucket "$BUCKET"
  aws_error NoSuchBucketPolicy 404 s3api get-bucket-policy --bucket "$BUCKET"
}

test_bucket_policy_malformed() {
  mkbucket
  aws_error MalformedPolicy 400 s3api put-bucket-policy --bucket "$BUCKET" --policy 'not json'
}

test_bucket_tagging_round_trip_and_delete() {
  mkbucket
  aws_ok s3api put-bucket-tagging --bucket "$BUCKET" --tagging 'TagSet=[{Key=env,Value=test},{Key=team,Value=core}]'
  aws_ok s3api get-bucket-tagging --bucket "$BUCKET"
  assert_jq_true '[.TagSet[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok s3api delete-bucket-tagging --bucket "$BUCKET"
  aws_error NoSuchTagSet 404 s3api get-bucket-tagging --bucket "$BUCKET"
}

test_bucket_cors_round_trip_and_delete() {
  mkbucket
  local cors='{"CORSRules":[{"AllowedOrigins":["https://app.example.com"],"AllowedMethods":["GET","PUT"],"AllowedHeaders":["*"],"MaxAgeSeconds":300}]}'
  aws_ok s3api put-bucket-cors --bucket "$BUCKET" --cors-configuration "$cors"
  aws_ok s3api get-bucket-cors --bucket "$BUCKET"
  assert_jq_true '.CORSRules == [{"AllowedHeaders":["*"],"AllowedMethods":["GET","PUT"],"AllowedOrigins":["https://app.example.com"],"MaxAgeSeconds":300}]'
  aws_ok s3api delete-bucket-cors --bucket "$BUCKET"
  aws_error NoSuchCORSConfiguration 404 s3api get-bucket-cors --bucket "$BUCKET"
}

test_bucket_lifecycle_round_trip_and_delete() {
  mkbucket
  local cfg='{"Rules":[{"ID":"expire-tmp","Status":"Enabled","Filter":{"Prefix":"tmp/"},"Expiration":{"Days":7}}]}'
  aws_ok s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" --lifecycle-configuration "$cfg"
  aws_ok s3api get-bucket-lifecycle-configuration --bucket "$BUCKET"
  assert_jq_true '.Rules == [{"ID":"expire-tmp","Filter":{"Prefix":"tmp/"},"Status":"Enabled","Expiration":{"Days":7}}]'
  aws_ok s3api delete-bucket-lifecycle --bucket "$BUCKET"
  aws_error NoSuchLifecycleConfiguration 404 s3api get-bucket-lifecycle-configuration --bucket "$BUCKET"
}

test_bucket_website_round_trip_and_delete() {
  mkbucket
  aws_ok s3api put-bucket-website --bucket "$BUCKET" \
    --website-configuration '{"IndexDocument":{"Suffix":"index.html"},"ErrorDocument":{"Key":"error.html"}}'
  aws_ok s3api get-bucket-website --bucket "$BUCKET"
  assert_jq_true '.IndexDocument.Suffix == "index.html" and .ErrorDocument.Key == "error.html"'
  aws_ok s3api delete-bucket-website --bucket "$BUCKET"
  aws_error NoSuchWebsiteConfiguration 404 s3api get-bucket-website --bucket "$BUCKET"
}

test_bucket_versioning_enable_and_suspend() {
  mkbucket
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
  aws_ok s3api get-bucket-versioning --bucket "$BUCKET"
  assert_jq '.Status' 'Enabled'
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Suspended
  aws_ok s3api get-bucket-versioning --bucket "$BUCKET"
  assert_jq '.Status' 'Suspended'
}

test_bucket_versioning_rejects_unknown_status() {
  mkbucket
  aws_error MalformedXML 400 s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=enabled
}

test_bucket_encryption_round_trip() {
  mkbucket
  aws_ok s3api put-bucket-encryption --bucket "$BUCKET" --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":false}]}'
  aws_ok s3api get-bucket-encryption --bucket "$BUCKET"
  assert_jq '.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm' 'AES256'
}

test_bucket_public_access_block_round_trip() {
  mkbucket
  aws_ok s3api put-public-access-block --bucket "$BUCKET" --public-access-block-configuration \
    BlockPublicAcls=false,IgnorePublicAcls=false,BlockPublicPolicy=true,RestrictPublicBuckets=true
  aws_ok s3api get-public-access-block --bucket "$BUCKET"
  assert_jq_true '.PublicAccessBlockConfiguration == {"BlockPublicAcls":false,"IgnorePublicAcls":false,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}'
}

test_bucket_ownership_controls_round_trip() {
  mkbucket
  aws_ok s3api put-bucket-ownership-controls --bucket "$BUCKET" --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerPreferred}]'
  aws_ok s3api get-bucket-ownership-controls --bucket "$BUCKET"
  assert_jq_true '.OwnershipControls.Rules == [{"ObjectOwnership":"BucketOwnerPreferred"}]'
}

test_bucket_logging_accelerate_request_payment_round_trips() {
  mkbucket
  aws_ok s3api put-bucket-accelerate-configuration --bucket "$BUCKET" --accelerate-configuration Status=Suspended
  aws_ok s3api get-bucket-accelerate-configuration --bucket "$BUCKET"
  assert_jq '.Status' 'Suspended'
  aws_ok s3api put-bucket-request-payment --bucket "$BUCKET" --request-payment-configuration Payer=Requester
  aws_ok s3api get-bucket-request-payment --bucket "$BUCKET"
  assert_jq '.Payer' 'Requester'
}

test_delete_bucket_removes_its_configuration() {
  local b
  b=$(new_name cli-delcfg)
  aws_ok s3api create-bucket --bucket "$b"
  aws_ok s3api put-bucket-tagging --bucket "$b" --tagging 'TagSet=[{Key=a,Value=b}]'
  aws_ok s3api delete-bucket --bucket "$b"
  aws_ok s3api create-bucket --bucket "$b"
  aws_error NoSuchTagSet 404 s3api get-bucket-tagging --bucket "$b"
}

# Objects: put, get, head, ranges, metadata, delete, copy

test_put_get_head_object() {
  mkbucket
  put_obj hello.txt 'hello s3'
  assert_jq '.ETag' "\"$(md5_hex 'hello s3')\""
  local out="$UE_TMP/get.out"
  aws_ok s3api get-object --bucket "$BUCKET" --key hello.txt "$out"
  assert_eq 'hello s3' "$(cat "$out")" 'object body'
  assert_jq '.ContentLength' '8'
  assert_jq '.ETag' "\"$(md5_hex 'hello s3')\""
  assert_jq '.AcceptRanges' 'bytes'
  assert_jq_match '.LastModified' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  aws_ok s3api head-object --bucket "$BUCKET" --key hello.txt
  assert_jq '.ContentLength' '8'
  assert_jq '.ETag' "\"$(md5_hex 'hello s3')\""
}

test_put_object_defaults_content_type_and_sse() {
  mkbucket
  put_obj plain.bin data
  aws_ok s3api head-object --bucket "$BUCKET" --key plain.bin
  assert_jq '.ContentType' 'binary/octet-stream'
  assert_jq '.ServerSideEncryption' 'AES256'
  assert_jq 'has("StorageClass")' 'false' 'STANDARD objects omit x-amz-storage-class'
}

test_put_object_user_metadata_is_lowercased() {
  mkbucket
  put_obj meta.txt x --metadata Color=Blue,Size=L
  aws_ok s3api head-object --bucket "$BUCKET" --key meta.txt
  assert_jq_true '.Metadata == {"color":"Blue","size":"L"}'
}

test_put_object_system_metadata_round_trips() {
  mkbucket
  put_obj sys.txt x --content-type text/plain --content-encoding gzip --content-language de \
    --content-disposition 'inline' --cache-control no-cache
  aws_ok s3api head-object --bucket "$BUCKET" --key sys.txt
  assert_jq '.ContentType' 'text/plain'
  assert_jq '.ContentEncoding' 'gzip'
  assert_jq '.ContentLanguage' 'de'
  assert_jq '.ContentDisposition' 'inline'
  assert_jq '.CacheControl' 'no-cache'
}

test_put_object_invalid_storage_class() {
  mkbucket
  aws_error InvalidStorageClass 400 s3api put-object --bucket "$BUCKET" --key k --body "$(empty_file)" --storage-class OUTPOSTS
}

test_put_object_storage_class_is_reported() {
  mkbucket
  put_obj ia.txt x --storage-class STANDARD_IA
  aws_ok s3api head-object --bucket "$BUCKET" --key ia.txt
  assert_jq '.StorageClass' 'STANDARD_IA'
}

test_get_object_missing_key() {
  mkbucket
  aws_error NoSuchKey 404 s3api get-object --bucket "$BUCKET" --key nope "$UE_TMP/nope.out"
  aws_error_message_contains 'The specified key does not exist.'
}

test_get_object_missing_bucket() {
  aws_error NoSuchBucket 404 s3api get-object --bucket "$(new_name cli-nobkt)" --key k "$UE_TMP/nobkt.out"
}

test_head_object_missing_key() {
  mkbucket
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key nope
}

test_object_keys_with_special_characters() {
  mkbucket
  local k
  for k in 'dir/sub dir/file name.txt' 'plus+sign' 'ünï/cödé.txt' 'a@b=c&d' 'percent%25.txt'; do
    put_obj "$k" "body of $k"
  done
  aws_ok s3api list-objects-v2 --bucket "$BUCKET"
  assert_jq_true '[.Contents[].Key] | sort == (["dir/sub dir/file name.txt","plus+sign","ünï/cödé.txt","a@b=c&d","percent%25.txt"] | sort)'
  local out="$UE_TMP/special.out"
  aws_ok s3api get-object --bucket "$BUCKET" --key 'plus+sign' "$out"
  assert_eq 'body of plus+sign' "$(cat "$out")"
  aws_ok s3api get-object --bucket "$BUCKET" --key 'dir/sub dir/file name.txt' "$out"
  assert_eq 'body of dir/sub dir/file name.txt' "$(cat "$out")"
}

test_get_object_range() {
  mkbucket
  put_obj r.txt '0123456789'
  local out="$UE_TMP/range.out"
  aws_ok s3api get-object --bucket "$BUCKET" --key r.txt --range bytes=2-5 "$out"
  assert_eq 2345 "$(cat "$out")"
  assert_jq '.ContentRange' 'bytes 2-5/10'
  assert_jq '.ContentLength' '4'
  aws_ok s3api get-object --bucket "$BUCKET" --key r.txt --range bytes=-3 "$out"
  assert_eq 789 "$(cat "$out")"
  assert_jq '.ContentRange' 'bytes 7-9/10'
  aws_ok s3api get-object --bucket "$BUCKET" --key r.txt --range bytes=8- "$out"
  assert_eq 89 "$(cat "$out")"
}

test_get_object_range_end_beyond_size_is_clipped() {
  mkbucket
  put_obj r.txt '0123456789'
  aws_ok s3api get-object --bucket "$BUCKET" --key r.txt --range bytes=5-100 "$UE_TMP/clip.out"
  assert_jq '.ContentRange' 'bytes 5-9/10'
}

test_get_object_range_not_satisfiable() {
  mkbucket
  put_obj r.txt '01234567'
  aws_error InvalidRange 416 s3api get-object --bucket "$BUCKET" --key r.txt --range bytes=1024-4096 "$UE_TMP/bad.out"
  aws_error_message_contains 'The requested range is not satisfiable'
}

test_get_object_if_none_match_etag_is_not_modified() {
  mkbucket
  put_obj c.txt cond
  local etag
  etag=$(jqv '.ETag')
  aws_error 304 304 s3api get-object --bucket "$BUCKET" --key c.txt --if-none-match "$etag" "$UE_TMP/c.out"
  aws_ok s3api get-object --bucket "$BUCKET" --key c.txt --if-none-match '"deadbeef"' "$UE_TMP/c.out"
}

test_get_object_if_match_mismatch_is_precondition_failed() {
  mkbucket
  put_obj c.txt cond
  aws_error PreconditionFailed 412 s3api get-object --bucket "$BUCKET" --key c.txt --if-match '"deadbeef"' "$UE_TMP/c.out"
  aws_error_message_contains 'At least one of the pre-conditions you specified did not hold'
  local etag
  aws_ok s3api head-object --bucket "$BUCKET" --key c.txt
  etag=$(jqv '.ETag')
  aws_ok s3api get-object --bucket "$BUCKET" --key c.txt --if-match "$etag" "$UE_TMP/c.out"
}

test_get_object_if_modified_since_future_date_is_ignored() {
  mkbucket
  put_obj c.txt cond
  aws_ok s3api get-object --bucket "$BUCKET" --key c.txt --if-modified-since 2099-01-01T00:00:00Z "$UE_TMP/c.out"
}

test_get_object_if_unmodified_since_before_write_fails() {
  mkbucket
  put_obj c.txt cond
  aws_error PreconditionFailed 412 s3api get-object --bucket "$BUCKET" --key c.txt --if-unmodified-since 2000-01-01T00:00:00Z "$UE_TMP/c.out"
}

test_put_object_if_none_match_star_conditional_write() {
  mkbucket
  put_obj once.txt first --if-none-match '*'
  aws_error PreconditionFailed 412 s3api put-object --bucket "$BUCKET" --key once.txt --body "$(empty_file)" --if-none-match '*'
  local out="$UE_TMP/once.out"
  aws_ok s3api get-object --bucket "$BUCKET" --key once.txt "$out"
  assert_eq first "$(cat "$out")" 'the losing write must not replace the object'
}

test_put_object_if_match_conditional_write() {
  mkbucket
  put_obj cas.txt v1
  local etag
  etag=$(jqv '.ETag')
  put_obj cas.txt v2 --if-match "$etag"
  aws_error PreconditionFailed 412 s3api put-object --bucket "$BUCKET" --key cas.txt --body "$(empty_file)" --if-match "$etag"
}

test_delete_object_is_idempotent() {
  mkbucket
  aws_ok s3api delete-object --bucket "$BUCKET" --key never-existed
  assert_eq 204 "$AWS_STATUS" 'DeleteObject status'
  put_obj d.txt x
  aws_ok s3api delete-object --bucket "$BUCKET" --key d.txt
  assert_eq 204 "$AWS_STATUS" 'DeleteObject status'
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key d.txt
}

test_delete_objects_reports_deleted_including_missing_keys() {
  mkbucket
  put_obj a 1
  put_obj b 2
  aws_ok s3api delete-objects --bucket "$BUCKET" --delete '{"Objects":[{"Key":"a"},{"Key":"b"},{"Key":"missing"}]}'
  assert_jq '[.Deleted[].Key] | sort | join(",")' 'a,b,missing'
  assert_jq '.Errors // [] | length' '0'
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate
  assert_jq '.KeyCount' '0'
}

test_delete_objects_quiet_mode() {
  mkbucket
  put_obj a 1
  aws_ok s3api delete-objects --bucket "$BUCKET" --delete '{"Objects":[{"Key":"a"}],"Quiet":true}'
  assert_jq '.Deleted // [] | length' '0'
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key a
}

test_copy_object_copies_content_and_metadata() {
  mkbucket
  put_obj src.txt 'copy body' --metadata Origin=source --content-type text/plain
  aws_ok s3api copy-object --bucket "$BUCKET" --key dst.txt --copy-source "$BUCKET/src.txt"
  assert_jq '.CopyObjectResult.ETag' "\"$(md5_hex 'copy body')\""
  assert_jq_match '.CopyObjectResult.LastModified' '^[0-9]{4}-'
  aws_ok s3api head-object --bucket "$BUCKET" --key dst.txt
  assert_jq_true '(.Metadata | with_entries(.key |= ascii_downcase)) == {"origin":"source"}' # key case: test_put_object_user_metadata_is_lowercased
  assert_jq '.ContentType' 'text/plain'
}

test_copy_object_metadata_directive_replace() {
  mkbucket
  put_obj src.txt 'body' --metadata Origin=source --content-type text/plain
  aws_ok s3api copy-object --bucket "$BUCKET" --key dst.txt --copy-source "$BUCKET/src.txt" \
    --metadata-directive REPLACE --metadata Replaced=yes
  aws_ok s3api head-object --bucket "$BUCKET" --key dst.txt
  assert_jq_true '(.Metadata | with_entries(.key |= ascii_downcase)) == {"replaced":"yes"}' # key case: test_put_object_user_metadata_is_lowercased
  assert_jq '.ContentType' 'binary/octet-stream' 'REPLACE without Content-Type resets it'
}

test_copy_object_onto_itself_is_rejected() {
  mkbucket
  put_obj same.txt x
  aws_error InvalidRequest 400 s3api copy-object --bucket "$BUCKET" --key same.txt --copy-source "$BUCKET/same.txt"
  aws_error_message_contains "This copy request is illegal because it is trying to copy an object to itself"
}

test_copy_object_onto_itself_with_replace_is_allowed() {
  mkbucket
  put_obj same.txt x
  aws_ok s3api copy-object --bucket "$BUCKET" --key same.txt --copy-source "$BUCKET/same.txt" \
    --metadata-directive REPLACE --metadata Touched=1
}

test_copy_object_missing_source_key() {
  mkbucket
  aws_error NoSuchKey 404 s3api copy-object --bucket "$BUCKET" --key dst --copy-source "$BUCKET/absent"
}

test_copy_object_source_condition_failure() {
  mkbucket
  put_obj src.txt x
  aws_error PreconditionFailed 412 s3api copy-object --bucket "$BUCKET" --key dst --copy-source "$BUCKET/src.txt" \
    --copy-source-if-match '"deadbeef"'
}

test_copy_object_special_character_source_key() {
  mkbucket
  put_obj 'we ird/ké y+1' 'special'
  # The CLI percent-encodes the copy source itself, so pass the raw key.
  aws_ok s3api copy-object --bucket "$BUCKET" --key plain --copy-source "$BUCKET/we ird/ké y+1"
  aws_ok s3api get-object --bucket "$BUCKET" --key plain "$UE_TMP/sp.out"
  assert_eq special "$(cat "$UE_TMP/sp.out")"
}

# Listings

seed_listing() {
  local k
  for k in a/1 a/2 b/1 c top; do put_obj "$k" "v-$k"; done
}

test_list_objects_v2_basic_shape() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate
  assert_jq '.KeyCount' '5'
  assert_jq '[.Contents[].Key] | join(",")' 'a/1,a/2,b/1,c,top' 'keys ascend by byte order'
  assert_jq '.Contents[0].Size' '5'
  assert_jq '.Contents[0].StorageClass' 'STANDARD'
  assert_jq '.Contents[0].ETag' "\"$(md5_hex 'v-a/1')\""
}

test_list_objects_v2_prefix() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --prefix a/
  assert_jq '[.Contents[].Key] | join(",")' 'a/1,a/2'
}

test_list_objects_v2_delimiter_common_prefixes() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --delimiter /
  assert_jq '[.CommonPrefixes[].Prefix] | join(",")' 'a/,b/'
  assert_jq '[.Contents[].Key] | join(",")' 'c,top'
  assert_jq '.KeyCount' '4' 'each common prefix counts as one key'
}

test_list_objects_v2_pagination_with_continuation_token() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --max-keys 2
  assert_jq '.IsTruncated' 'true'
  assert_jq '[.Contents[].Key] | join(",")' 'a/1,a/2'
  local token
  token=$(jqv '.NextContinuationToken')
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --max-keys 2 --continuation-token "$token"
  assert_jq '[.Contents[].Key] | join(",")' 'b/1,c'
  assert_jq '.ContinuationToken' "$token"
  token=$(jqv '.NextContinuationToken')
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --max-keys 2 --continuation-token "$token"
  assert_jq '[.Contents[].Key] | join(",")' 'top'
  assert_jq '.IsTruncated' 'false'
}

test_list_objects_v2_start_after() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --start-after b/1
  assert_jq '[.Contents[].Key] | join(",")' 'c,top'
}

test_list_objects_v2_owner_only_with_fetch_owner() {
  mkbucket
  put_obj k v
  aws_ok s3api list-objects-v2 --bucket "$BUCKET"
  assert_jq '.Contents[0] | has("Owner")' 'false'
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --fetch-owner
  assert_jq_match '.Contents[0].Owner.ID' '^.+$'
}

test_list_objects_v2_max_keys_zero() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --max-keys 0
  assert_jq '.KeyCount' '0'
  assert_jq '.IsTruncated' 'false'
}

test_list_objects_v2_invalid_continuation_token() {
  mkbucket
  put_obj k v
  aws_error InvalidArgument 400 s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --continuation-token not-a-real-token
  aws_error_message_contains 'The continuation token provided is incorrect'
}

test_list_objects_v2_missing_bucket() {
  aws_error NoSuchBucket 404 s3api list-objects-v2 --bucket "$(new_name cli-nolist)"
}

test_list_objects_v1_marker_pagination() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects --bucket "$BUCKET" --no-paginate --max-keys 2
  assert_jq '.IsTruncated' 'true'
  assert_jq '[.Contents[].Key] | join(",")' 'a/1,a/2'
  assert_jq 'has("NextMarker")' 'false' 'NextMarker is only returned when a delimiter was sent'
  aws_ok s3api list-objects --bucket "$BUCKET" --no-paginate --max-keys 2 --marker a/2
  assert_jq '[.Contents[].Key] | join(",")' 'b/1,c'
}

test_list_objects_v1_next_marker_with_delimiter() {
  mkbucket
  seed_listing
  aws_ok s3api list-objects --bucket "$BUCKET" --no-paginate --max-keys 1 --delimiter /
  assert_jq '.IsTruncated' 'true'
  assert_jq '.NextMarker' 'a/'
}

test_list_objects_invalid_encoding_type() {
  mkbucket
  aws_error InvalidArgument 400 s3api list-objects-v2 --bucket "$BUCKET" --no-paginate --encoding-type bogus
}

# Versioning

test_versioned_bucket_keeps_versions() {
  mkbucket
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
  put_obj v.txt first
  local v1
  v1=$(jqv '.VersionId')
  assert_match '^[A-Za-z0-9._]{32}$' "$v1" 'VersionId'
  put_obj v.txt second
  local v2
  v2=$(jqv '.VersionId')
  assert_ne "$v1" "$v2" 'a new version per write'
  aws_ok s3api get-object --bucket "$BUCKET" --key v.txt --version-id "$v1" "$UE_TMP/v1.out"
  assert_eq first "$(cat "$UE_TMP/v1.out")"
  aws_ok s3api get-object --bucket "$BUCKET" --key v.txt "$UE_TMP/v2.out"
  assert_eq second "$(cat "$UE_TMP/v2.out")"
  assert_jq '.VersionId' "$v2"
  aws_ok s3api list-object-versions --bucket "$BUCKET"
  assert_jq '.Versions | length' '2'
  assert_jq '.Versions | map(select(.IsLatest))[0].VersionId' "$v2"
}

test_versioned_delete_adds_delete_marker() {
  mkbucket
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
  put_obj m.txt data
  local v1
  v1=$(jqv '.VersionId')
  aws_ok s3api delete-object --bucket "$BUCKET" --key m.txt
  assert_jq '.DeleteMarker' 'true'
  local marker
  marker=$(jqv '.VersionId')
  aws_error NoSuchKey 404 s3api get-object --bucket "$BUCKET" --key m.txt "$UE_TMP/m.out"
  aws_ok s3api list-object-versions --bucket "$BUCKET"
  assert_jq '.DeleteMarkers | length' '1'
  assert_jq '.DeleteMarkers[0].IsLatest' 'true'
  assert_jq '.Versions[0].VersionId' "$v1"
  aws_ok s3api delete-object --bucket "$BUCKET" --key m.txt --version-id "$marker"
  aws_ok s3api get-object --bucket "$BUCKET" --key m.txt "$UE_TMP/m.out"
  assert_eq data "$(cat "$UE_TMP/m.out")" 'removing the delete marker restores the object'
}

test_versioned_get_unknown_version_id() {
  mkbucket
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
  put_obj v.txt x
  aws_error NoSuchVersion 404 s3api get-object --bucket "$BUCKET" --key v.txt --version-id aaaaaaaaaabbbbbbbbbbccccccccccdd "$UE_TMP/v.out"
}

test_unversioned_bucket_rejects_version_id() {
  mkbucket
  put_obj v.txt x
  aws_error InvalidArgument 400 s3api get-object --bucket "$BUCKET" --key v.txt --version-id notnull "$UE_TMP/v.out"
  aws_error_message_contains 'Invalid version id specified'
}

test_versioned_bucket_delete_requires_empty_versions() {
  mkbucket
  aws_ok s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
  put_obj v.txt x
  aws_ok s3api delete-object --bucket "$BUCKET" --key v.txt
  aws_error BucketNotEmpty 409 s3api delete-bucket --bucket "$BUCKET"
  aws_error_message_contains 'You must delete all versions in the bucket.'
}

# Multipart uploads

# start_mpu KEY [create-multipart-upload args...]: sets UPLOAD_ID.
start_mpu() {
  local key=$1
  shift
  aws_ok s3api create-multipart-upload --bucket "$BUCKET" --key "$key" "$@"
  UPLOAD_ID=$(jqv '.UploadId')
  assert_match '^[A-Za-z0-9._~-]{20,}$' "$UPLOAD_ID" 'UploadId'
}

test_multipart_upload_complete() {
  mkbucket
  local p1="$UE_TMP/part1" p2="$UE_TMP/part2" whole="$UE_TMP/whole" out="$UE_TMP/mp.out"
  make_file "$p1" 5242880
  make_file "$p2" 1024
  cat "$p1" "$p2" >"$whole"
  start_mpu mp.bin
  aws_ok s3api upload-part --bucket "$BUCKET" --key mp.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p1"
  local e1
  e1=$(jqv '.ETag')
  assert_eq "\"$(md5sum "$p1" | cut -d' ' -f1)\"" "$e1" 'part ETag is the part MD5'
  aws_ok s3api upload-part --bucket "$BUCKET" --key mp.bin --upload-id="$UPLOAD_ID" --part-number 2 --body "$p2"
  local e2
  e2=$(jqv '.ETag')
  aws_ok s3api complete-multipart-upload --bucket "$BUCKET" --key mp.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1},{\"PartNumber\":2,\"ETag\":$e2}]}"
  assert_jq '.Bucket' "$BUCKET"
  assert_jq '.Key' 'mp.bin'
  local expected
  expected=$(python3 -I -c '
import hashlib, sys
d = [hashlib.md5(open(p, "rb").read()).digest() for p in sys.argv[1:]]
print(hashlib.md5(b"".join(d)).hexdigest() + "-" + str(len(d)))' "$p1" "$p2")
  assert_jq '.ETag' "\"$expected\""
  aws_ok s3api get-object --bucket "$BUCKET" --key mp.bin "$out"
  cmp "$whole" "$out" || fail 'completed multipart object differs from the concatenated parts'
}

test_multipart_list_parts_and_uploads() {
  mkbucket
  local p1="$UE_TMP/lp1"
  make_file "$p1" 5242880
  start_mpu listed.bin
  aws_ok s3api upload-part --bucket "$BUCKET" --key listed.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p1"
  aws_ok s3api list-parts --bucket "$BUCKET" --key listed.bin --upload-id="$UPLOAD_ID"
  assert_jq '.Parts | length' '1'
  assert_jq '.Parts[0].PartNumber' '1'
  assert_jq '.Parts[0].Size' '5242880'
  aws_ok s3api list-multipart-uploads --bucket "$BUCKET"
  assert_jq '.Uploads | length' '1'
  assert_jq '.Uploads[0].Key' 'listed.bin'
  assert_jq '.Uploads[0].UploadId' "$UPLOAD_ID"
  aws_ok s3api list-objects-v2 --bucket "$BUCKET" --no-paginate
  assert_jq '.KeyCount' '0' 'in-progress uploads are not listed as objects'
}

test_multipart_abort() {
  mkbucket
  start_mpu aborted.bin
  aws_ok s3api abort-multipart-upload --bucket "$BUCKET" --key aborted.bin --upload-id="$UPLOAD_ID"
  assert_eq 204 "$AWS_STATUS" 'AbortMultipartUpload status'
  aws_error NoSuchUpload 404 s3api list-parts --bucket "$BUCKET" --key aborted.bin --upload-id="$UPLOAD_ID"
  aws_error NoSuchUpload 404 s3api abort-multipart-upload --bucket "$BUCKET" --key aborted.bin --upload-id="$UPLOAD_ID"
}

test_multipart_non_final_part_below_minimum_size() {
  mkbucket
  start_mpu small.bin
  local p="$UE_TMP/tiny"
  make_file "$p" 1024
  aws_ok s3api upload-part --bucket "$BUCKET" --key small.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api upload-part --bucket "$BUCKET" --key small.bin --upload-id="$UPLOAD_ID" --part-number 2 --body "$p"
  local e2
  e2=$(jqv '.ETag')
  aws_error EntityTooSmall 400 s3api complete-multipart-upload --bucket "$BUCKET" --key small.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1},{\"PartNumber\":2,\"ETag\":$e2}]}"
}

test_multipart_parts_out_of_order() {
  mkbucket
  start_mpu order.bin
  local p="$UE_TMP/ord"
  make_file "$p" 5242880
  aws_ok s3api upload-part --bucket "$BUCKET" --key order.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api upload-part --bucket "$BUCKET" --key order.bin --upload-id="$UPLOAD_ID" --part-number 2 --body "$p"
  local e2
  e2=$(jqv '.ETag')
  aws_error InvalidPartOrder 400 s3api complete-multipart-upload --bucket "$BUCKET" --key order.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":2,\"ETag\":$e2},{\"PartNumber\":1,\"ETag\":$e1}]}"
}

test_multipart_unknown_part_etag() {
  mkbucket
  start_mpu wrongetag.bin
  local p="$UE_TMP/we"
  make_file "$p" 1024
  aws_ok s3api upload-part --bucket "$BUCKET" --key wrongetag.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p"
  aws_error InvalidPart 400 s3api complete-multipart-upload --bucket "$BUCKET" --key wrongetag.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload '{"Parts":[{"PartNumber":1,"ETag":"\"00000000000000000000000000000000\""}]}'
}

test_multipart_upload_part_number_out_of_range() {
  mkbucket
  start_mpu range.bin
  aws_error InvalidArgument 400 s3api upload-part --bucket "$BUCKET" --key range.bin --upload-id="$UPLOAD_ID" --part-number 10001 --body "$(empty_file)"
  aws_error_message_contains 'Part number must be an integer between 1 and 10000, inclusive'
}

test_multipart_upload_part_unknown_upload_id() {
  mkbucket
  aws_error NoSuchUpload 404 s3api upload-part --bucket "$BUCKET" --key k --upload-id bogus-upload-id --part-number 1 --body "$(empty_file)"
}

test_multipart_object_head_by_part_number() {
  mkbucket
  local p1="$UE_TMP/hp1" p2="$UE_TMP/hp2"
  make_file "$p1" 5242880
  make_file "$p2" 2048
  start_mpu parts.bin
  aws_ok s3api upload-part --bucket "$BUCKET" --key parts.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p1"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api upload-part --bucket "$BUCKET" --key parts.bin --upload-id="$UPLOAD_ID" --part-number 2 --body "$p2"
  local e2
  e2=$(jqv '.ETag')
  aws_ok s3api complete-multipart-upload --bucket "$BUCKET" --key parts.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1},{\"PartNumber\":2,\"ETag\":$e2}]}"
  aws_ok s3api head-object --bucket "$BUCKET" --key parts.bin --part-number 2
  assert_eq 206 "$AWS_STATUS" 'HeadObject with partNumber'
  assert_jq '.ContentLength' '2048'
  assert_jq '.PartsCount' '2'
}

test_multipart_metadata_comes_from_create() {
  mkbucket
  local p="$UE_TMP/md"
  make_file "$p" 1024
  start_mpu meta.bin --content-type application/x-custom --metadata Origin=mpu --tagging 'k=v'
  aws_ok s3api upload-part --bucket "$BUCKET" --key meta.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api complete-multipart-upload --bucket "$BUCKET" --key meta.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1}]}"
  aws_ok s3api head-object --bucket "$BUCKET" --key meta.bin
  assert_jq '.ContentType' 'application/x-custom'
  assert_jq_true '(.Metadata | with_entries(.key |= ascii_downcase)) == {"origin":"mpu"}' # key case: test_put_object_user_metadata_is_lowercased
  aws_ok s3api get-object-tagging --bucket "$BUCKET" --key meta.bin
  assert_jq_true '.TagSet == [{"Key":"k","Value":"v"}]'
}

# Object tagging

test_object_tagging_crud() {
  mkbucket
  put_obj t.txt x
  aws_ok s3api put-object-tagging --bucket "$BUCKET" --key t.txt --tagging 'TagSet=[{Key=env,Value=test},{Key=team,Value=core}]'
  aws_ok s3api get-object-tagging --bucket "$BUCKET" --key t.txt
  assert_jq_true '[.TagSet[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok s3api delete-object-tagging --bucket "$BUCKET" --key t.txt
  assert_eq 204 "$AWS_STATUS" 'DeleteObjectTagging status'
  aws_ok s3api get-object-tagging --bucket "$BUCKET" --key t.txt
  assert_jq '.TagSet | length' '0'
}

test_object_tags_from_put_header() {
  mkbucket
  put_obj t.txt x --tagging 'a=1&b=2'
  aws_ok s3api get-object-tagging --bucket "$BUCKET" --key t.txt
  assert_jq_true '[.TagSet[] | {(.Key): .Value}] | add == {"a":"1","b":"2"}'
  aws_ok s3api get-object --bucket "$BUCKET" --key t.txt "$UE_TMP/t.out"
  assert_jq '.TagCount' '2'
}

test_overwriting_an_object_clears_its_tags() {
  mkbucket
  put_obj t.txt x --tagging 'a=1'
  put_obj t.txt y
  aws_ok s3api get-object-tagging --bucket "$BUCKET" --key t.txt
  assert_jq '.TagSet | length' '0'
}

test_object_tagging_does_not_change_etag() {
  mkbucket
  put_obj t.txt x
  local before
  before=$(jqv '.ETag')
  aws_ok s3api put-object-tagging --bucket "$BUCKET" --key t.txt --tagging 'TagSet=[{Key=a,Value=b}]'
  aws_ok s3api head-object --bucket "$BUCKET" --key t.txt
  assert_jq '.ETag' "$before"
}

test_object_tagging_header_with_duplicate_keys_is_rejected() {
  mkbucket
  aws_error InvalidArgument 400 s3api put-object --bucket "$BUCKET" --key t.txt --body "$(empty_file)" --tagging 'a=1&a=2'
}

test_object_tagging_missing_key() {
  mkbucket
  aws_error NoSuchKey 404 s3api get-object-tagging --bucket "$BUCKET" --key absent
}

# Checksums and content digests

# check_put_with_checksum ALGORITHM JQ-FIELD: put with the algorithm, expect the
# server to return and store the digest the client computes independently.
check_put_with_checksum() {
  local alg=$1 field=$2 f="$UE_TMP/ck.bin" expected
  mkbucket
  printf 'checksum me, please' >"$f"
  expected=$(checksum_b64 "$alg" "$f")
  aws_ok s3api put-object --bucket "$BUCKET" --key ck.bin --body "$f" --checksum-algorithm "$alg"
  assert_jq ".$field" "$expected" "stored $alg checksum"
  aws_ok s3api head-object --bucket "$BUCKET" --key ck.bin --checksum-mode ENABLED
  assert_jq ".$field" "$expected" "HeadObject $alg checksum"
  aws_ok s3api get-object --bucket "$BUCKET" --key ck.bin --checksum-mode ENABLED "$UE_TMP/ck.out"
  assert_jq ".$field" "$expected" "GetObject $alg checksum"
}

test_checksum_crc32() { check_put_with_checksum CRC32 ChecksumCRC32; }
test_checksum_crc32c() { check_put_with_checksum CRC32C ChecksumCRC32C; }
test_checksum_crc64nvme() { check_put_with_checksum CRC64NVME ChecksumCRC64NVME; }
test_checksum_sha1() { check_put_with_checksum SHA1 ChecksumSHA1; }
test_checksum_sha256() { check_put_with_checksum SHA256 ChecksumSHA256; }

test_checksum_value_supplied_by_client_is_verified() {
  mkbucket
  local f="$UE_TMP/ckv.bin"
  printf 'verify me' >"$f"
  aws_ok s3api put-object --bucket "$BUCKET" --key ok.bin --body "$f" --checksum-sha256 "$(checksum_b64 SHA256 "$f")"
  aws_error BadDigest 400 s3api put-object --bucket "$BUCKET" --key bad.bin --body "$f" \
    --checksum-sha256 "$(printf 'something else' | sha256sum | cut -d' ' -f1 | xxd -r -p | base64)"
  aws_error 404 404 s3api head-object --bucket "$BUCKET" --key bad.bin
}

test_content_md5_mismatch_is_bad_digest() {
  mkbucket
  local f="$UE_TMP/md5.bin"
  printf 'md5 body' >"$f"
  aws_error BadDigest 400 s3api put-object --bucket "$BUCKET" --key k --body "$f" \
    --content-md5 "$(printf 'other body' | md5sum | cut -d' ' -f1 | xxd -r -p | base64)"
  aws_error_message_contains 'The Content-MD5 you specified did not match what we received.'
}

test_content_md5_malformed_is_invalid_digest() {
  mkbucket
  local f="$UE_TMP/md5b.bin"
  printf 'md5 body' >"$f"
  aws_error InvalidDigest 400 s3api put-object --bucket "$BUCKET" --key k --body "$f" --content-md5 '__invalid__'
  aws_error_message_contains 'The Content-MD5 you specified was invalid.'
}

test_content_md5_correct_is_accepted() {
  mkbucket
  local f="$UE_TMP/md5ok.bin"
  printf 'md5 body' >"$f"
  aws_ok s3api put-object --bucket "$BUCKET" --key k --body "$f" \
    --content-md5 "$(printf 'md5 body' | md5sum | cut -d' ' -f1 | xxd -r -p | base64)"
}

test_multipart_with_part_checksums() {
  mkbucket
  start_mpu ckmp.bin --checksum-algorithm SHA256
  local p="$UE_TMP/ckp1"
  make_file "$p" 5242880
  local sum
  sum=$(checksum_b64 SHA256 "$p")
  aws_ok s3api upload-part --bucket "$BUCKET" --key ckmp.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p" --checksum-algorithm SHA256
  assert_jq '.ChecksumSHA256' "$sum"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api complete-multipart-upload --bucket "$BUCKET" --key ckmp.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1,\"ChecksumSHA256\":\"$sum\"}]}"
  assert_jq_match '.ChecksumSHA256' '^.+-1$'
}

# SSE-KMS (cross-service with KMS)

mkkey() {
  aws_ok kms create-key --description "s3 conformance $(new_name key)"
  KEY_ID=$(jqv '.KeyMetadata.KeyId')
  KEY_ARN=$(jqv '.KeyMetadata.Arn')
}

test_sse_kms_put_reports_key_arn() {
  mkbucket
  mkkey
  put_obj enc.txt secret --server-side-encryption aws:kms --ssekms-key-id "$KEY_ID"
  assert_jq '.ServerSideEncryption' 'aws:kms'
  assert_jq '.SSEKMSKeyId' "$KEY_ARN" 'the response names the full key ARN even when the request used the key id'
  aws_ok s3api head-object --bucket "$BUCKET" --key enc.txt
  assert_jq '.ServerSideEncryption' 'aws:kms'
  assert_jq '.SSEKMSKeyId' "$KEY_ARN"
  aws_ok s3api get-object --bucket "$BUCKET" --key enc.txt "$UE_TMP/enc.out"
  assert_eq secret "$(cat "$UE_TMP/enc.out")"
}

test_sse_kms_etag_is_not_content_md5() {
  mkbucket
  mkkey
  put_obj enc.txt secret --server-side-encryption aws:kms --ssekms-key-id "$KEY_ARN"
  assert_jq_match '.ETag' '^"[0-9a-f]{32}"$'
  assert_ne "\"$(md5_hex secret)\"" "$(jqv '.ETag')" 'SSE-KMS ETags are not the content MD5'
}

test_sse_kms_bucket_default_encryption() {
  mkbucket
  mkkey
  aws_ok s3api put-bucket-encryption --bucket "$BUCKET" --server-side-encryption-configuration \
    "{\"Rules\":[{\"ApplyServerSideEncryptionByDefault\":{\"SSEAlgorithm\":\"aws:kms\",\"KMSMasterKeyID\":\"$KEY_ARN\"},\"BucketKeyEnabled\":true}]}"
  aws_ok s3api get-bucket-encryption --bucket "$BUCKET"
  assert_jq '.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.KMSMasterKeyID' "$KEY_ARN"
  put_obj enc.txt secret
  assert_jq '.ServerSideEncryption' 'aws:kms'
  assert_jq '.SSEKMSKeyId' "$KEY_ARN"
  assert_jq '.BucketKeyEnabled' 'true'
}

test_sse_kms_unknown_key() {
  mkbucket
  aws_error KMS.NotFoundException 400 s3api put-object --bucket "$BUCKET" --key k --body "$(empty_file)" \
    --server-side-encryption aws:kms --ssekms-key-id 11111111-2222-3333-4444-555555555555
  aws_error_message_contains "does not exist"
}

test_sse_kms_malformed_key_id() {
  mkbucket
  aws_error KMS.NotFoundException 400 s3api put-object --bucket "$BUCKET" --key k --body "$(empty_file)" \
    --server-side-encryption aws:kms --ssekms-key-id fake-key-id
  aws_error_message_contains "Invalid keyId 'fake-key-id'"
}

test_sse_kms_disabled_key() {
  mkbucket
  mkkey
  aws_ok kms disable-key --key-id "$KEY_ID"
  aws_error KMS.DisabledException 400 s3api put-object --bucket "$BUCKET" --key k --body "$(empty_file)" \
    --server-side-encryption aws:kms --ssekms-key-id "$KEY_ID"
  aws_error_message_contains "$KEY_ARN is disabled."
}

test_sse_kms_key_pending_deletion() {
  mkbucket
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  aws_error KMS.KMSInvalidStateException 400 s3api put-object --bucket "$BUCKET" --key k --body "$(empty_file)" \
    --server-side-encryption aws:kms --ssekms-key-id "$KEY_ID"
  aws_error_message_contains "$KEY_ARN is pending deletion."
}

test_get_sse_kms_object_after_key_disabled() {
  mkbucket
  mkkey
  put_obj enc.txt secret --server-side-encryption aws:kms --ssekms-key-id "$KEY_ID"
  aws_ok kms disable-key --key-id "$KEY_ID"
  aws_error KMS.DisabledException 400 s3api get-object --bucket "$BUCKET" --key enc.txt "$UE_TMP/enc.out"
}

# Addressing styles

test_virtual_hosted_style_addressing() {
  mkbucket
  put_obj vh.txt 'virtual hosted'
  local cfg port
  cfg=$(mktemp "$UE_TMP/awsconfig.XXXXXX")
  printf '[default]\ns3 =\n    addressing_style = virtual\n' >"$cfg"
  port=${AWS_ENDPOINT_URL##*:}
  AWS_CONFIG_FILE=$cfg AWS_ENDPOINT_URL="http://localhost:$port" aws_ok s3 cp "s3://$BUCKET/vh.txt" -
  assert_eq 'virtual hosted' "$AWS_OUT"
  assert_contains "$BUCKET.localhost:$port" "$(cat "$AWS_ERR")" 'the request must go to the bucket host name'
}

test_virtual_hosted_style_listing_and_put() {
  mkbucket
  local cfg port src="$UE_TMP/vhput.txt"
  cfg=$(mktemp "$UE_TMP/awsconfig.XXXXXX")
  printf '[default]\ns3 =\n    addressing_style = virtual\n' >"$cfg"
  port=${AWS_ENDPOINT_URL##*:}
  printf 'put through the bucket host' >"$src"
  AWS_CONFIG_FILE=$cfg AWS_ENDPOINT_URL="http://localhost:$port" aws_ok s3 cp "$src" "s3://$BUCKET/dir/put.txt"
  AWS_CONFIG_FILE=$cfg AWS_ENDPOINT_URL="http://localhost:$port" aws_ok s3 ls "s3://$BUCKET/dir/"
  assert_contains 'put.txt' "$AWS_OUT"
  aws_ok s3api head-object --bucket "$BUCKET" --key dir/put.txt
  assert_jq '.ContentLength' '27'
}

# Event notifications to SQS (should-have)

# notify_setup EVENTS-JSON [FILTER-JSON]: a queue the bucket may publish to, wired
# to BUCKET. Sets QUEUE_URL and drains the s3:TestEvent.
notify_setup() {
  local events=$1 filter=${2:-null} queue_name queue_arn policy acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  queue_name=$(new_name cli-s3evt)
  aws_ok sqs create-queue --queue-name "$queue_name"
  QUEUE_URL=$(jqv '.QueueUrl')
  queue_arn="arn:aws:sqs:${AWS_REGION}:${acct}:${queue_name}"
  policy=$(jq -nc --arg q "$queue_arn" --arg b "arn:aws:s3:::$BUCKET" '{
    Version:"2012-10-17",
    Statement:[{Effect:"Allow",Principal:{Service:"s3.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,
                Condition:{ArnLike:{"aws:SourceArn":$b}}}]}')
  aws_ok sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes "$(jq -nc --arg p "$policy" '{Policy:$p}')"
  local cfg
  cfg=$(jq -nc --arg q "$queue_arn" --argjson e "$events" --argjson f "$filter" '
    {QueueConfigurations:[({Id:"conformance",QueueArn:$q,Events:$e} + (if $f == null then {} else {Filter:$f} end))]}')
  aws_ok s3api put-bucket-notification-configuration --bucket "$BUCKET" --notification-configuration "$cfg"
  # S3 sends one s3:TestEvent to a new queue destination.
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 3 --max-number-of-messages 10
  assert_jq_true '[.Messages[]?.Body | fromjson | select(.Event == "s3:TestEvent")] | length == 1'
}

# next_event: AWS_OUT becomes the first S3 event record received (within ~6s).
next_event() {
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 5 --max-number-of-messages 1
  assert_jq '.Messages | length' '1' 'event message'
  AWS_OUT=$(jqv '.Messages[0].Body')
}

test_notification_config_round_trip() {
  mkbucket
  notify_setup '["s3:ObjectCreated:*"]' '{"Key":{"FilterRules":[{"Name":"prefix","Value":"in/"},{"Name":"suffix","Value":".csv"}]}}'
  aws_ok s3api get-bucket-notification-configuration --bucket "$BUCKET"
  assert_jq '.QueueConfigurations | length' '1'
  assert_jq '.QueueConfigurations[0].Id' 'conformance'
  assert_jq_true '.QueueConfigurations[0].Events == ["s3:ObjectCreated:*"]'
  assert_jq_true '.QueueConfigurations[0].Filter.Key.FilterRules | map({Name, Value}) | sort_by(.Name) == [{"Name":"Prefix","Value":"in/"},{"Name":"Suffix","Value":".csv"}]'
}

test_notification_object_created_event() {
  mkbucket
  notify_setup '["s3:ObjectCreated:*"]'
  put_obj 'uploads/a b@c.txt' 'event body'
  local etag
  etag=$(md5_hex 'event body')
  next_event
  assert_jq '.Records | length' '1'
  assert_jq '.Records[0].eventSource' 'aws:s3'
  assert_jq '.Records[0].eventName' 'ObjectCreated:Put'
  assert_jq '.Records[0].awsRegion' "$AWS_REGION"
  assert_jq '.Records[0].s3.bucket.name' "$BUCKET"
  assert_jq '.Records[0].s3.bucket.arn' "arn:aws:s3:::$BUCKET"
  assert_jq '.Records[0].s3.configurationId' 'conformance'
  assert_jq '.Records[0].s3.object.key' 'uploads/a+b%40c.txt' 'object keys are URL-encoded in events'
  assert_jq '.Records[0].s3.object.size' '10'
  assert_jq '.Records[0].s3.object.eTag' "$etag" 'event ETags carry no quotes'
  assert_jq_match '.Records[0].eventTime' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}\.[0-9]{3}Z$'
}

test_notification_object_removed_event() {
  mkbucket
  notify_setup '["s3:ObjectRemoved:*"]'
  put_obj gone.txt x
  aws_ok s3api delete-object --bucket "$BUCKET" --key gone.txt
  next_event
  assert_jq '.Records[0].eventName' 'ObjectRemoved:Delete'
  assert_jq '.Records[0].s3.object.key' 'gone.txt'
  assert_jq '.Records[0].s3.object | has("size")' 'false'
}

test_notification_filter_by_prefix_and_suffix() {
  mkbucket
  notify_setup '["s3:ObjectCreated:*"]' '{"Key":{"FilterRules":[{"Name":"prefix","Value":"in/"},{"Name":"suffix","Value":".csv"}]}}'
  put_obj 'in/skip.txt' x
  put_obj 'out/skip.csv' x
  put_obj 'in/take.csv' x
  next_event
  assert_jq '.Records[0].s3.object.key' 'in/take.csv'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 2
  assert_jq '.Messages // [] | length' '0' 'non-matching keys produce no event'
}

test_notification_copy_and_multipart_event_names() {
  mkbucket
  notify_setup '["s3:ObjectCreated:*"]'
  put_obj src.txt x
  next_event
  aws_ok s3api copy-object --bucket "$BUCKET" --key dst.txt --copy-source "$BUCKET/src.txt"
  next_event
  assert_jq '.Records[0].eventName' 'ObjectCreated:Copy'
  local p="$UE_TMP/evp"
  make_file "$p" 1024
  start_mpu mp.bin
  aws_ok s3api upload-part --bucket "$BUCKET" --key mp.bin --upload-id="$UPLOAD_ID" --part-number 1 --body "$p"
  local e1
  e1=$(jqv '.ETag')
  aws_ok s3api complete-multipart-upload --bucket "$BUCKET" --key mp.bin --upload-id="$UPLOAD_ID" \
    --multipart-upload "{\"Parts\":[{\"PartNumber\":1,\"ETag\":$e1}]}"
  next_event
  assert_jq '.Records[0].eventName' 'ObjectCreated:CompleteMultipartUpload'
}

test_notification_to_missing_queue_is_rejected() {
  mkbucket
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  aws_error InvalidArgument 400 s3api put-bucket-notification-configuration --bucket "$BUCKET" --notification-configuration \
    "{\"QueueConfigurations\":[{\"QueueArn\":\"arn:aws:sqs:${AWS_REGION}:${acct}:$(new_name cli-s3ghost)\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
  aws_error_message_contains 'Unable to validate the following destination configurations'
}

test_notification_malformed_queue_arn() {
  mkbucket
  aws_error InvalidArgument 400 s3api put-bucket-notification-configuration --bucket "$BUCKET" --notification-configuration \
    '{"QueueConfigurations":[{"QueueArn":"not-an-arn","Events":["s3:ObjectCreated:*"]}]}'
  aws_error_message_contains 'The ARN could not be parsed'
}
