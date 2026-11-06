# SSM Parameter Store: what roadmap item 9f's Terraform done check names
# (docs/research/ssm-parameters.md 10.1, 10.2, 12.5): String, StringList,
# SecureString under the default key and under a KMS alias, tags,
# overwrite = true, the Advanced tier, Intelligent-Tiering with a small and
# a 5000-character value, and both data sources. The update step changes
# values and descriptions, and turns a root SecureString into a String in
# place, as the provider's acceptance test does against AWS
# (parameter_test.go:L872-L901).

# Fixture: the customer managed key a SecureString names by alias.
resource "aws_kms_key" "ssm" {
  description             = "${var.name_prefix} parameters"
  deletion_window_in_days = 7
}

resource "aws_kms_alias" "ssm" {
  name          = "alias/${var.name_prefix}-parameters"
  target_key_id = aws_kms_key.ssm.key_id
}

resource "aws_ssm_parameter" "string" {
  name        = "/${var.name_prefix}/app/endpoint"
  type        = "String"
  value       = local.updated ? "https://api.example.com/v2" : "https://api.example.com/v1"
  description = local.updated ? "API endpoint (v2)" : "API endpoint"
}

resource "aws_ssm_parameter" "list" {
  name  = "/${var.name_prefix}/app/regions"
  type  = "StringList"
  value = local.updated ? "us-east-1,eu-west-1" : "us-east-1"
}

resource "aws_ssm_parameter" "secret_default_key" {
  name  = "/${var.name_prefix}/app/db-password"
  type  = "SecureString"
  value = local.updated ? "conformance-2" : "conformance-1"
}

resource "aws_ssm_parameter" "secret_alias" {
  name   = "/${var.name_prefix}/app/api-key"
  type   = "SecureString"
  key_id = aws_kms_alias.ssm.name
  value  = "key-material"
}

resource "aws_ssm_parameter" "tagged" {
  name  = "/${var.name_prefix}/app/flags"
  type  = "String"
  value = "on"

  tags = merge(
    { Purpose = "feature-flags" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_ssm_parameter" "overwrite" {
  name      = "/${var.name_prefix}/app/overwritten"
  type      = "String"
  value     = local.updated ? "second" : "first"
  overwrite = true
}

resource "aws_ssm_parameter" "advanced" {
  name  = "/${var.name_prefix}/app/advanced"
  type  = "String"
  tier  = "Advanced"
  value = "advanced"
}

resource "aws_ssm_parameter" "intelligent_small" {
  name  = "/${var.name_prefix}/tiering/small"
  type  = "String"
  tier  = "Intelligent-Tiering"
  value = "small"
}

resource "aws_ssm_parameter" "intelligent_large" {
  name  = "/${var.name_prefix}/tiering/large"
  type  = "String"
  tier  = "Intelligent-Tiering"
  # 5000 characters, over Standard's 4 KB, so the tier resolves to Advanced
  # (ssm-parameters.md 4.2); range() caps its result at 1024 elements.
  value = join("", [for i in range(1000) : "xxxxx"])
}

# A root parameter, created with a leading slash: the type change of the
# update step is allowed for root names (ssm-parameters.md 4.1 [T]).
resource "aws_ssm_parameter" "root" {
  name  = "/${var.name_prefix}-root"
  type  = local.updated ? "String" : "SecureString"
  value = "root-value"
}

data "aws_ssm_parameter" "string" {
  name = aws_ssm_parameter.string.name
}

data "aws_ssm_parameters_by_path" "app" {
  path      = "/${var.name_prefix}/app"
  recursive = true

  depends_on = [
    aws_ssm_parameter.string,
    aws_ssm_parameter.list,
    aws_ssm_parameter.secret_default_key,
    aws_ssm_parameter.secret_alias,
    aws_ssm_parameter.tagged,
    aws_ssm_parameter.overwrite,
    aws_ssm_parameter.advanced,
  ]
}

output "string_value" {
  value = data.aws_ssm_parameter.string.insecure_value
}

output "app_parameter_names" {
  value = data.aws_ssm_parameters_by_path.app.names
}

# No output reads the Intelligent-Tiering parameters' tier: the provider's
# Read stores the resolved tier from DescribeParameters (Standard or
# Advanced, never Intelligent-Tiering; ssm-parameters.md 4.2), and only
# the resource's DiffSuppress hides the change (provider v6.68.0
# internal/service/ssm/parameter.go:L136-L147, L376), so a root output of
# it moves on the first refresh against AWS too and the plan after apply
# is not empty.
