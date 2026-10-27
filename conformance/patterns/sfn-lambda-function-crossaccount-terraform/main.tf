terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.68.0"
    }
  }
}


variable "region" {}
variable "prefix" {}

# Configure the AWS Provider
provider "aws" {
 region  = "${var.region}"
 alias   = "default"
}
 
#cross account
provider "aws" {
 access_key = "200000900001"
 secret_key = "understudy"
 region  = "${var.region}"
 alias   = "crossaccount"
}