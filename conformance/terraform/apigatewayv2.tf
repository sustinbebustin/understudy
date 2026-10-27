# API Gateway HTTP API (apigatewayv2): one Python function behind a Lambda
# proxy integration in each payload format, an SQS-SendMessage service
# integration (stored; a request reaching it answers 501), routes, a
# $default stage with automatic deployment, and a named stage serving an
# explicit deployment (apigateway-http.md 13.6). The update step changes the
# API's description and tags, the 2.0 integration's timeout, the named
# stage's variables, and adds a route, which replaces the deployment: the
# deployment's triggers force a new one, create_before_destroy creates it and
# repoints the stage before the old one is deleted, since understudy refuses
# to delete a deployment a stage serves. run.sh calls the routes through the
# path fallback, /_understudy/execute-api/<id>/..., with the id taken from
# api_endpoint (invoke_url always names amazonaws.com, 9.7).

data "aws_iam_policy_document" "apigatewayv2_lambda_assume" {
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
resource "aws_iam_role" "apigatewayv2_lambda" {
  name               = "${var.name_prefix}-exec"
  assume_role_policy = data.aws_iam_policy_document.apigatewayv2_lambda_assume.json
}

data "aws_iam_policy_document" "apigatewayv2_sqs_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["apigateway.amazonaws.com"]
    }
  }
}

# Fixture: the role the SQS integration names and the queue it targets.
resource "aws_iam_role" "apigatewayv2_sqs" {
  name               = "${var.name_prefix}-sqs"
  assume_role_policy = data.aws_iam_policy_document.apigatewayv2_sqs_assume.json
}

resource "aws_sqs_queue" "apigatewayv2" {
  name = "${var.name_prefix}-orders"
}

data "archive_file" "apigatewayv2_handler" {
  type        = "zip"
  source_file = "${var.functions_dir}/python-http-api/handler.py"
  output_path = "${var.artifacts_dir}/apigatewayv2-handler.zip"
}

resource "aws_cloudwatch_log_group" "apigatewayv2_handler" {
  name              = "/aws/lambda/${var.name_prefix}-handler"
  retention_in_days = 7
}

# Fixture: the function both Lambda integrations invoke.
resource "aws_lambda_function" "apigatewayv2" {
  function_name    = "${var.name_prefix}-handler"
  role             = aws_iam_role.apigatewayv2_lambda.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.apigatewayv2_handler.output_path
  source_code_hash = data.archive_file.apigatewayv2_handler.output_base64sha256
  timeout          = 10

  depends_on = [aws_cloudwatch_log_group.apigatewayv2_handler]
}

resource "aws_apigatewayv2_api" "this" {
  name          = "${var.name_prefix}-api"
  protocol_type = "HTTP"
  description   = local.updated ? "Conformance HTTP API (updated)" : "Conformance HTTP API"

  tags = merge(
    { Purpose = "conformance" },
    local.updated ? { Revision = "2" } : {},
  )

  lifecycle {
    # The provider builds execution_arn from its own account, and with
    # skip_credentials_validation it has none (apigateway-http.md 9.7, the
    # 2026-10-27 run), so the permission below builds its source ARN from
    # data.aws_caller_identity instead.
    postcondition {
      condition     = self.execution_arn == "arn:${local.partition}:execute-api:${local.region}::${self.id}"
      error_message = "execution_arn is ${self.execution_arn}; apigateway-http.md 9.7 expects arn:${local.partition}:execute-api:${local.region}::${self.id} (no account) under this provider block."
    }
  }
}

# integration_method is left out: Terraform suppresses the POST AWS reads
# back for a Lambda proxy integration (apigateway-http.md 9.3).
resource "aws_apigatewayv2_integration" "lambda_v2" {
  api_id                 = aws_apigatewayv2_api.this.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.apigatewayv2.invoke_arn
  payload_format_version = "2.0"
  timeout_milliseconds   = local.updated ? 15000 : 29000
}

resource "aws_apigatewayv2_integration" "lambda_v1" {
  api_id                 = aws_apigatewayv2_api.this.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.apigatewayv2.arn
  payload_format_version = "1.0"
}

resource "aws_apigatewayv2_integration" "sqs" {
  api_id              = aws_apigatewayv2_api.this.id
  integration_type    = "AWS_PROXY"
  integration_subtype = "SQS-SendMessage"
  credentials_arn     = aws_iam_role.apigatewayv2_sqs.arn

  request_parameters = {
    QueueUrl    = aws_sqs_queue.apigatewayv2.url
    MessageBody = "$request.body"
  }
}

resource "aws_apigatewayv2_route" "hello" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "GET /hello"
  target    = "integrations/${aws_apigatewayv2_integration.lambda_v2.id}"
}

resource "aws_apigatewayv2_route" "pets" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "GET /pets/{name}"
  target    = "integrations/${aws_apigatewayv2_integration.lambda_v1.id}"
}

resource "aws_apigatewayv2_route" "orders" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "POST /orders"
  target    = "integrations/${aws_apigatewayv2_integration.sqs.id}"
}

# Added by the update; the named stage serves it only once the replacement
# deployment is in place.
resource "aws_apigatewayv2_route" "extra" {
  count = local.updated ? 1 : 0

  api_id    = aws_apigatewayv2_api.this.id
  route_key = "GET /extra"
  target    = "integrations/${aws_apigatewayv2_integration.lambda_v2.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.this.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_apigatewayv2_deployment" "live" {
  api_id      = aws_apigatewayv2_api.this.id
  description = "Conformance deployment"

  # The hash covers the attributes that change what the API serves, not
  # whole resource objects: the provider leaves empty maps such as
  # request_models null in state after a create and reads them back as {}
  # on the next refresh, whatever the server answers, which would change a
  # hash of the objects and replace the deployment on every second plan.
  triggers = {
    redeployment = sha1(jsonencode([
      [
        for r in concat([aws_apigatewayv2_route.hello, aws_apigatewayv2_route.pets, aws_apigatewayv2_route.orders], aws_apigatewayv2_route.extra) :
        [r.route_key, r.target, r.authorization_type]
      ],
      [
        for i in [aws_apigatewayv2_integration.lambda_v2, aws_apigatewayv2_integration.lambda_v1, aws_apigatewayv2_integration.sqs] :
        [i.id, i.integration_uri, i.integration_subtype, i.payload_format_version, i.timeout_milliseconds, i.request_parameters == null ? {} : i.request_parameters]
      ],
    ]))
  }

  lifecycle {
    create_before_destroy = true
  }

  # A deployment snapshots the routes that exist when it is made.
  depends_on = [
    aws_apigatewayv2_route.hello,
    aws_apigatewayv2_route.pets,
    aws_apigatewayv2_route.orders,
    aws_apigatewayv2_route.extra,
  ]
}

resource "aws_apigatewayv2_stage" "live" {
  api_id        = aws_apigatewayv2_api.this.id
  name          = "live"
  deployment_id = aws_apigatewayv2_deployment.live.id

  stage_variables = {
    color = local.updated ? "green" : "blue"
  }

  tags = {
    Purpose = "conformance"
  }
}

resource "aws_lambda_permission" "apigatewayv2" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.apigatewayv2.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "arn:${local.partition}:execute-api:${local.region}:${local.account_id}:${aws_apigatewayv2_api.this.id}/*/*"
}

output "apigatewayv2_api_endpoint" {
  value = aws_apigatewayv2_api.this.api_endpoint
}

output "apigatewayv2_live_deployment_id" {
  value = aws_apigatewayv2_deployment.live.id
}
