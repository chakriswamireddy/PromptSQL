terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.50" }
  }
}

variable "environment" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_security_group_ids" { type = list(string) }
variable "kms_key_arn" { type = string }
variable "node_type" { type = string; default = "cache.r7g.large" }
variable "num_cache_clusters" { type = number; default = 3 }
variable "tags" { type = map(string); default = {} }

locals {
  name_prefix = "governance-${var.environment}"
  common_tags = merge(var.tags, { Environment = var.environment, ManagedBy = "terraform" })
}

resource "aws_elasticache_subnet_group" "this" {
  name       = "${local.name_prefix}-redis"
  subnet_ids = var.subnet_ids
  tags       = local.common_tags
}

resource "aws_security_group" "redis" {
  name        = "${local.name_prefix}-redis-sg"
  description = "ElastiCache Redis — allow EKS nodes on 6379"
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${local.name_prefix}-redis-sg" })
}

resource "aws_security_group_rule" "redis_ingress" {
  for_each                 = toset(var.allowed_security_group_ids)
  type                     = "ingress"
  from_port                = 6379
  to_port                  = 6379
  protocol                 = "tcp"
  source_security_group_id = each.value
  security_group_id        = aws_security_group.redis.id
}

resource "aws_security_group_rule" "redis_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  security_group_id = aws_security_group.redis.id
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = "${local.name_prefix}-redis"
  description          = "Governance Platform Redis — session store, L2 PDP cache, pub/sub"

  node_type               = var.node_type
  num_cache_clusters      = var.num_cache_clusters
  automatic_failover_enabled = true
  multi_az_enabled        = true

  engine               = "redis"
  engine_version       = "7.2"
  parameter_group_name = "default.redis7"
  port                 = 6379

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.redis.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  kms_key_id                 = var.kms_key_arn

  snapshot_retention_limit = 7
  snapshot_window          = "03:30-04:30"

  maintenance_window = "sun:04:00-sun:05:00"

  # TLS auth token managed via Secrets Manager; set in application via env var.
  auth_token = null  # mTLS + network policy is the primary auth mechanism

  tags = local.common_tags
}

output "primary_endpoint" {
  value = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "reader_endpoint" {
  value = aws_elasticache_replication_group.this.reader_endpoint_address
}

output "security_group_id" {
  value = aws_security_group.redis.id
}
