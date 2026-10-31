# shellcheck shell=bash
# The apigw-lambda-rekognition pattern's documented test, sourced by
# conformance/patterns/run.sh, which defines pattern_aws and endpoint.
#
#   pattern_check DIR    README, Testing, step 2: a POST of
#                        {"object_name": "image.png", "content_type":
#                        "image/png"} with Content-Type text/plain to the
#                        api_gateway_endpoint_url output answers 200 with
#                        {"presigned_url": ...}, an S3 PUT URL for that key in
#                        the pattern's bucket, signed in either query form
#                        (botocore chooses SigV2 Signature= or SigV4
#                        X-Amz-Signature=, which is the client's choice and
#                        not the emulator's). The output is the stage's
#                        invoke_url on amazonaws.com; the check calls the same
#                        stage and resource through understudy's path form,
#                        <endpoint>/_understudy/execute-api/<api-id>/dev/generate-presigned-url
#                        (docs/research/apigateway-rest.md 12.6). The method is
#                        an AWS_PROXY Lambda integration, so the function's
#                        statusCode and body are the answer (5.2).
#
# Steps 1, 3 and 4 are not run: the SNS e-mail subscription needs a mailbox,
# and the function signs its URL for https://s3.<region>.amazonaws.com
# (generate_presigned_url.zip), so the PUT would leave the machine and the
# Rekognition call after it is never reached (apigateway-rest.md 12.7).

pattern_check() {
  local api_id out status body
  api_id=$(pattern_aws apigateway get-rest-apis \
    --query "items[?name=='understudy-presigned-url-api'].id" --output text)
  if [[ ! "$api_id" =~ ^[a-z0-9]{10}$ ]]; then
    echo "get-rest-apis found no API named understudy-presigned-url-api: '$api_id'"
    return 1
  fi

  # shellcheck disable=SC2154 # endpoint is set by run.sh
  out=$(curl -sS -w '\n%{http_code}' --location \
    "$endpoint/_understudy/execute-api/$api_id/dev/generate-presigned-url" \
    --header 'Content-Type: text/plain' --data '{"object_name": "image.png", "content_type": "image/png"}')
  status=${out##*$'\n'}
  body=${out%$'\n'*}
  if [[ "$status" != 200 ]] || ! jq -e '
    (keys == ["presigned_url"])
    and (.presigned_url | test("understudy-s3-upload"))
    and (.presigned_url | test("image\\.png\\?"))
    and (.presigned_url | test("[?&](X-Amz-)?Signature="))' <<<"$body" >/dev/null 2>&1; then
    echo "POST /dev/generate-presigned-url answered $status $body, want 200 and a presigned_url for understudy-s3-upload/image.png"
    return 1
  fi
}
