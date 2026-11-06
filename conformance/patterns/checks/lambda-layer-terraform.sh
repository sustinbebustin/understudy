# shellcheck shell=bash
# The lambda-layer-terraform pattern's documented steps, sourced by
# conformance/patterns/run.sh, which defines pattern_aws.
#
#   pattern_prepare DIR  README, Deployment Instructions, step 3: install
#                        mysql-connector-python into
#                        dependencies/mysql-connector-python/python. uv
#                        installs the wheel for the functions' runtime,
#                        Python 3.13 on x86_64, whatever Python this host
#                        runs.
#   pattern_check DIR    README, Testing: the function with the layer runs
#                        and lists /opt, where Lambda extracts the layer
#                        (docs/research/lambda-destinations-layers.md 8.1,
#                        8.2); the function without it fails with
#                        "Unable to import module 'app': No module named 'mysql'".

pattern_prepare() {
  local dir=$1
  uv pip install --quiet --target "$dir/dependencies/mysql-connector-python/python" \
    --python-version 3.13 --python-platform x86_64-manylinux_2_28 --only-binary :all: \
    -r "$dir/dependencies/mysql-connector-python/requirements.txt"
}

pattern_check() {
  local dir=$1 out
  out=$(mktemp "$dir/invoke.XXXXXX")

  pattern_aws lambda invoke --function-name lambda_with_layer --cli-binary-format raw-in-base64-out --payload '{}' "$out" >"$out.meta"
  if [[ "$(jq -r '.FunctionError // empty' "$out.meta")" != "" ]]; then
    echo "lambda_with_layer failed: $(cat "$out")"
    return 1
  fi
  if ! jq -e 'type == "array" and (index("/opt/python/mysql/connector/__init__.py") != null)' "$out" >/dev/null; then
    echo "lambda_with_layer listed $(head -c 400 "$out"), want /opt/python/mysql/connector/__init__.py among the files under /opt"
    return 1
  fi

  pattern_aws lambda invoke --function-name lambda_without_layer --cli-binary-format raw-in-base64-out --payload '{}' "$out" >"$out.meta"
  if [[ "$(jq -r '.FunctionError // empty' "$out.meta")" != "Unhandled" ]]; then
    echo "lambda_without_layer answered $(cat "$out.meta"), want FunctionError Unhandled"
    return 1
  fi
  if [[ "$(jq -r '.errorMessage' "$out")" != "Unable to import module 'app': No module named 'mysql'" ]]; then
    echo "lambda_without_layer failed with $(cat "$out"), want Unable to import module 'app': No module named 'mysql'"
    return 1
  fi
}
