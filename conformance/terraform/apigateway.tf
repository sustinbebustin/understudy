# API Gateway REST API (apigateway, v1): one Python function behind AWS_PROXY
# methods on /items/{id} (GET) and /{proxy+} (ANY), a MOCK OPTIONS method on
# /items/{id} with a method response and an integration response carrying a
# static header and a literal template, a deployment and a stage
# (apigateway-rest.md 9). The update step changes the API's description and
# tags, the GET integration's timeout and the stage's variables, and adds a
# MOCK GET /status, which replaces the deployment: its triggers force a new
# one, create_before_destroy creates it and repoints the stage before the old
# one is deleted, since a deployment a stage serves cannot be deleted
# (apigateway-rest.md 3.6, 9.4). run.sh calls the methods through the path
# fallback, /_understudy/execute-api/<id>/<stage>/..., with the API id from
# the rest_api resource (invoke_url always names amazonaws.com, 9.2).

data "aws_iam_policy_document" "apigateway_lambda_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# Fixture: the function's execution role.
resource "aws_iam_role" "apigateway_lambda" {
  name               = "${var.name_prefix}-exec"
  assume_role_policy = data.aws_iam_policy_document.apigateway_lambda_assume.json
}

# The HTTP API's handler answers a REST 1.0 event too: the event has no
# version key, so it reports the matched resource (apigateway-rest.md 5.1).
data "archive_file" "apigateway_handler" {
  type        = "zip"
  source_file = "${var.functions_dir}/python-http-api/handler.py"
  output_path = "${var.artifacts_dir}/apigateway-handler.zip"
}

resource "aws_cloudwatch_log_group" "apigateway_handler" {
  name              = "/aws/lambda/${var.name_prefix}-handler"
  retention_in_days = 7
}

# Fixture: the function both AWS_PROXY methods invoke.
resource "aws_lambda_function" "apigateway" {
  function_name    = "${var.name_prefix}-handler"
  role             = aws_iam_role.apigateway_lambda.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.apigateway_handler.output_path
  source_code_hash = data.archive_file.apigateway_handler.output_base64sha256
  timeout          = 10

  depends_on = [aws_cloudwatch_log_group.apigateway_handler]
}

resource "aws_api_gateway_rest_api" "this" {
  name        = "${var.name_prefix}-api"
  description = local.updated ? "Conformance REST API (updated)" : "Conformance REST API"

  endpoint_configuration {
    types = ["REGIONAL"]
  }

  tags = merge(
    { Purpose = "conformance" },
    local.updated ? { Revision = "2" } : {},
  )
}

resource "aws_api_gateway_resource" "items" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_rest_api.this.root_resource_id
  path_part   = "items"
}

resource "aws_api_gateway_resource" "item" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.items.id
  path_part   = "{id}"
}

resource "aws_api_gateway_resource" "proxy" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_rest_api.this.root_resource_id
  path_part   = "{proxy+}"
}

resource "aws_api_gateway_method" "item_get" {
  rest_api_id   = aws_api_gateway_rest_api.this.id
  resource_id   = aws_api_gateway_resource.item.id
  http_method   = "GET"
  authorization = "NONE"

  request_parameters = {
    "method.request.path.id" = true
  }
}

# A Lambda proxy integration is always called with POST (apigateway-rest.md
# 3.4), and integration_http_method must read back as sent (9.3 item 4).
resource "aws_api_gateway_integration" "item_get" {
  rest_api_id             = aws_api_gateway_rest_api.this.id
  resource_id             = aws_api_gateway_resource.item.id
  http_method             = aws_api_gateway_method.item_get.http_method
  type                    = "AWS_PROXY"
  integration_http_method = "POST"
  uri                     = aws_lambda_function.apigateway.invoke_arn
  timeout_milliseconds    = local.updated ? 15000 : 29000
}

resource "aws_api_gateway_method" "proxy_any" {
  rest_api_id   = aws_api_gateway_rest_api.this.id
  resource_id   = aws_api_gateway_resource.proxy.id
  http_method   = "ANY"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "proxy_any" {
  rest_api_id             = aws_api_gateway_rest_api.this.id
  resource_id             = aws_api_gateway_resource.proxy.id
  http_method             = aws_api_gateway_method.proxy_any.http_method
  type                    = "AWS_PROXY"
  integration_http_method = "POST"
  uri                     = aws_lambda_function.apigateway.invoke_arn
}

# MOCK OPTIONS: the request template's statusCode selects the 200
# integration response, which maps a static header and a literal body
# (apigateway-rest.md 6.3, 6.7). MOCK drops integration_http_method, so it is
# left out (9.3 item 4).
resource "aws_api_gateway_method" "item_options" {
  rest_api_id   = aws_api_gateway_rest_api.this.id
  resource_id   = aws_api_gateway_resource.item.id
  http_method   = "OPTIONS"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "item_options" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.item.id
  http_method = aws_api_gateway_method.item_options.http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = jsonencode({ statusCode = 200 })
  }
}

resource "aws_api_gateway_method_response" "item_options" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.item.id
  http_method = aws_api_gateway_method.item_options.http_method
  status_code = "200"

  response_parameters = {
    "method.response.header.Access-Control-Allow-Methods" = true
  }

  response_models = {
    "application/json" = "Empty"
  }
}

resource "aws_api_gateway_integration_response" "item_options" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.item.id
  http_method = aws_api_gateway_method.item_options.http_method
  status_code = aws_api_gateway_method_response.item_options.status_code

  response_parameters = {
    "method.response.header.Access-Control-Allow-Methods" = "'GET,OPTIONS'"
  }

  response_templates = {
    "application/json" = jsonencode({ preflight = true })
  }

  depends_on = [aws_api_gateway_integration.item_options]
}

# Added by the update; the stage serves it only once the replacement
# deployment is in place.
resource "aws_api_gateway_resource" "status" {
  count = local.updated ? 1 : 0

  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_rest_api.this.root_resource_id
  path_part   = "status"
}

resource "aws_api_gateway_method" "status" {
  count = local.updated ? 1 : 0

  rest_api_id   = aws_api_gateway_rest_api.this.id
  resource_id   = aws_api_gateway_resource.status[0].id
  http_method   = "GET"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "status" {
  count = local.updated ? 1 : 0

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.status[0].id
  http_method = aws_api_gateway_method.status[0].http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = jsonencode({ statusCode = 200 })
  }
}

resource "aws_api_gateway_method_response" "status" {
  count = local.updated ? 1 : 0

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.status[0].id
  http_method = aws_api_gateway_method.status[0].http_method
  status_code = "200"
}

resource "aws_api_gateway_integration_response" "status" {
  count = local.updated ? 1 : 0

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = aws_api_gateway_resource.status[0].id
  http_method = aws_api_gateway_method.status[0].http_method
  status_code = aws_api_gateway_method_response.status[0].status_code

  response_templates = {
    "application/json" = jsonencode({ status = "ok" })
  }

  depends_on = [aws_api_gateway_integration.status]
}

resource "aws_api_gateway_deployment" "live" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  description = "Conformance deployment"

  # The hash covers the attributes that change what the API serves
  # (apigateway-rest.md 9.4), named one by one rather than whole objects,
  # which can read back differently from what the create left in state.
  triggers = {
    redeployment = sha1(jsonencode([
      [
        for r in concat([aws_api_gateway_resource.items, aws_api_gateway_resource.item, aws_api_gateway_resource.proxy], aws_api_gateway_resource.status) :
        [r.id, r.path]
      ],
      [
        for m in concat([aws_api_gateway_method.item_get, aws_api_gateway_method.proxy_any, aws_api_gateway_method.item_options], aws_api_gateway_method.status) :
        [m.id, m.authorization]
      ],
      [
        for i in concat([aws_api_gateway_integration.item_get, aws_api_gateway_integration.proxy_any, aws_api_gateway_integration.item_options], aws_api_gateway_integration.status) :
        [i.id, i.type, i.uri, i.timeout_milliseconds, i.request_templates == null ? {} : i.request_templates]
      ],
      [
        for r in concat([aws_api_gateway_integration_response.item_options], aws_api_gateway_integration_response.status) :
        [r.id, r.response_templates == null ? {} : r.response_templates, r.response_parameters == null ? {} : r.response_parameters]
      ],
    ]))
  }

  lifecycle {
    create_before_destroy = true
  }

  # A deployment snapshots the API as it is when it is made (9.4).
  depends_on = [
    aws_api_gateway_integration.item_get,
    aws_api_gateway_integration.proxy_any,
    aws_api_gateway_integration_response.item_options,
    aws_api_gateway_integration_response.status,
  ]
}

resource "aws_api_gateway_stage" "live" {
  rest_api_id   = aws_api_gateway_rest_api.this.id
  stage_name    = "live"
  deployment_id = aws_api_gateway_deployment.live.id

  variables = {
    color = local.updated ? "green" : "blue"
  }

  tags = {
    Purpose = "conformance"
  }
}

# The provider builds execution_arn from its own account, which it lacks
# under skip_credentials_validation (README), so the source ARN is built
# from data.aws_caller_identity, as apigatewayv2.tf does.
resource "aws_lambda_permission" "apigateway" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.apigateway.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "arn:${local.partition}:execute-api:${local.region}:${local.account_id}:${aws_api_gateway_rest_api.this.id}/*/*"
}

output "apigateway_rest_api_id" {
  value = aws_api_gateway_rest_api.this.id
}

output "apigateway_live_deployment_id" {
  value = aws_api_gateway_deployment.live.id
}
