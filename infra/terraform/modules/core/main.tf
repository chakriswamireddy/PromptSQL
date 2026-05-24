terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.50"
    }
  }
}

variable "environment" {
  type        = string
  description = "Deployment environment: dev | staging | prod"
  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be dev, staging, or prod"
  }
}

variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "vpc_cidr" {
  type    = string
  default = "10.0.0.0/16"
}

locals {
  name_prefix = "governance-${var.environment}"
  common_tags = {
    Environment = var.environment
    ManagedBy   = "terraform"
    Project     = "governance-platform"
  }
}

# ── VPC ───────────────────────────────────────────────────────────────────────
resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags = merge(local.common_tags, { Name = "${local.name_prefix}-vpc" })
}

# ── KMS CMK (referenced by the kms module, exported here) ────────────────────
resource "aws_kms_key" "main" {
  description             = "${local.name_prefix} envelope encryption key"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  tags                    = local.common_tags
}

resource "aws_kms_alias" "main" {
  name          = "alias/${local.name_prefix}-cmk"
  target_key_id = aws_kms_key.main.key_id
}

# ── CloudWatch log group for VPC flow logs ───────────────────────────────────
resource "aws_cloudwatch_log_group" "vpc_flow_logs" {
  name              = "/aws/vpc/${local.name_prefix}/flow-logs"
  retention_in_days = 90
  kms_key_id        = aws_kms_key.main.arn
  tags              = local.common_tags
}

output "vpc_id"      { value = aws_vpc.main.id }
output "cmk_arn"     { value = aws_kms_key.main.arn }
output "cmk_key_id"  { value = aws_kms_key.main.key_id }
