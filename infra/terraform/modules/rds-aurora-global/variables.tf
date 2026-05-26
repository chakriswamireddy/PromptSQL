variable "environment" {
  type = string
  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be dev, staging, or prod"
  }
}

variable "primary_region" {
  type        = string
  description = "AWS region for the Aurora global cluster writer"
}

variable "secondary_regions" {
  type        = list(string)
  description = "AWS regions for read-only Aurora global cluster members"
  default     = []
}

variable "vpc_id" {
  type = string
}

variable "db_subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs for the DB subnet group (must span ≥ 2 AZs)"
}

variable "allowed_security_group_ids" {
  type        = list(string)
  description = "EC2 SG IDs allowed to connect to port 5432 (e.g. EKS node SG)"
}

variable "db_name" {
  type    = string
  default = "governance"
}

variable "engine_version" {
  type    = string
  default = "16.2"
}

variable "instance_class" {
  type    = string
  default = "db.r7g.large"
}

variable "min_capacity" {
  type    = number
  default = 0.5
  description = "Aurora Serverless v2 minimum ACU (0.5 = ~1 GB)"
}

variable "max_capacity" {
  type    = number
  default = 64
  description = "Aurora Serverless v2 maximum ACU"
}

variable "backup_retention_days" {
  type    = number
  default = 30
}

variable "deletion_protection" {
  type    = bool
  default = true
}

variable "kms_key_arn" {
  type        = string
  description = "KMS key ARN for storage encryption"
}

variable "tags" {
  type    = map(string)
  default = {}
}
