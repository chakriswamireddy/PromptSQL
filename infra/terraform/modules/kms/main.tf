terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.50"
    }
  }
}

variable "environment"  { type = string }
variable "service_name" { type = string }
variable "cmk_arn"      { type = string }

locals {
  name_prefix = "governance-${var.environment}-${var.service_name}"
}

# Per-service data key — envelope encrypted with the env CMK.
resource "aws_kms_key" "service" {
  description             = "${local.name_prefix} data key"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "Enable IAM User Permissions"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root" }
        Action    = "kms:*"
        Resource  = "*"
      }
    ]
  })
}

data "aws_caller_identity" "current" {}

output "key_arn" { value = aws_kms_key.service.arn }
output "key_id"  { value = aws_kms_key.service.key_id }
