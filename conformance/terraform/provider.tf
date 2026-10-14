# Shared by every service configuration. run.sh copies this file next to one
# <service>.tf into a working directory of its own, so each service applies,
# plans and destroys as an independent root module with its own state.

terraform {
  required_version = ">= 1.16.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.68.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "2.7.1"
    }
  }
}

# The endpoint comes from AWS_ENDPOINT_URL, which scripts/sealed sets to the
# loopback understudy listener, and the credentials from the dummy
# AWS_ACCESS_KEY_ID it sets. The skip flags are the ones the provider documents
# for custom endpoints (docs/research/tf-messaging.md 1.2); path-style S3
# is needed because the endpoint is an IP literal.
provider "aws" {
  region = var.region

  s3_use_path_style           = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  default_tags {
    tags = {
      "conformance-suite" = "terraform"
    }
  }
}

variable "step" {
  description = "Which scenario to apply: create is the initial configuration, update changes several attributes of every resource."
  type        = string

  validation {
    condition     = contains(["create", "update"], var.step)
    error_message = "step must be create or update."
  }
}

variable "name_prefix" {
  description = "Prefix of every resource name, unique per service."
  type        = string
}

variable "run_id" {
  description = "Random suffix for globally unique names such as S3 buckets."
  type        = string
}

variable "region" {
  description = "Region the provider signs for."
  type        = string
  default     = "us-east-1"
}

variable "functions_dir" {
  description = "Absolute path of the Lambda function sources (conformance/terraform/functions)."
  type        = string
}

variable "artifacts_dir" {
  description = "Absolute path where run.sh put the built Go bootstrap and where Lambda zips are written."
  type        = string
}

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

data "aws_region" "current" {}

locals {
  updated    = var.step == "update"
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition
  region     = data.aws_region.current.region
}
