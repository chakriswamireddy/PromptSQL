terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.50" }
  }
  backend "s3" {
    bucket         = "governance-platform-tfstate"
    key            = "prod-us-east-1/terraform.tfstate"
    region         = "us-east-1"
    encrypt        = true
    dynamodb_table = "governance-platform-tfstate-lock"
  }
}

provider "aws" {
  region = "us-east-1"
  default_tags { tags = local.common_tags }
}

locals {
  environment  = "prod"
  aws_region   = "us-east-1"
  cluster_name = "governance-prod-us-east-1"
  common_tags = {
    Environment = local.environment
    Region      = local.aws_region
    ManagedBy   = "terraform"
    Project     = "governance-platform"
  }
}

# ── Core networking (VPC, subnets, NAT GW) ────────────────────────────────────
module "core" {
  source      = "../../modules/core"
  environment = local.environment
  aws_region  = local.aws_region
  vpc_cidr    = "10.1.0.0/16"
}

# ── KMS CMK for all regional resources ───────────────────────────────────────
module "kms" {
  source      = "../../modules/kms"
  environment = local.environment
}

# ── EKS cluster ───────────────────────────────────────────────────────────────
module "eks" {
  source             = "../../modules/eks"
  environment        = local.environment
  aws_region         = local.aws_region
  cluster_name       = local.cluster_name
  kubernetes_version = "1.30"
  vpc_id             = module.core.vpc_id
  private_subnet_ids = module.core.private_subnet_ids
  tags               = local.common_tags
}

# ── Aurora PostgreSQL Global (writer in us-east-1) ────────────────────────────
module "aurora" {
  source       = "../../modules/rds-aurora-global"
  environment  = local.environment
  primary_region = local.aws_region
  vpc_id       = module.core.vpc_id
  db_subnet_ids = module.core.database_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn  = module.kms.key_arn
  instance_class = "db.serverless"
  min_capacity = 1
  max_capacity = 128
  tags         = local.common_tags
}

# ── ElastiCache Redis ─────────────────────────────────────────────────────────
module "redis" {
  source                    = "../../modules/elasticache"
  environment               = local.environment
  vpc_id                    = module.core.vpc_id
  subnet_ids                = module.core.private_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn               = module.kms.key_arn
  node_type                 = "cache.r7g.large"
  num_cache_clusters        = 3
  tags                      = local.common_tags
}

# ── MSK Kafka ─────────────────────────────────────────────────────────────────
module "msk" {
  source                    = "../../modules/msk"
  environment               = local.environment
  vpc_id                    = module.core.vpc_id
  subnet_ids                = module.core.private_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn               = module.kms.key_arn
  broker_instance_type      = "kafka.m7g.large"
  number_of_broker_nodes    = 3
  kafka_version             = "3.7.x"
  tags                      = local.common_tags
}

# ── S3 WORM bucket (audit cold storage) ──────────────────────────────────────
resource "aws_s3_bucket" "audit_worm" {
  bucket = "governance-platform-audit-worm-prod-us-east-1"
  tags   = local.common_tags
}

resource "aws_s3_bucket_versioning" "audit_worm" {
  bucket = aws_s3_bucket.audit_worm.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_object_lock_configuration" "audit_worm" {
  bucket = aws_s3_bucket.audit_worm.id
  rule {
    default_retention {
      mode  = "COMPLIANCE"
      years = 7
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "audit_worm" {
  bucket = aws_s3_bucket.audit_worm.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = module.kms.key_arn
    }
  }
}

resource "aws_s3_bucket_public_access_block" "audit_worm" {
  bucket                  = aws_s3_bucket.audit_worm.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Cross-Region Replication to eu-west-1 for compliance (Phase 15.6)
resource "aws_s3_bucket_replication_configuration" "audit_worm" {
  bucket = aws_s3_bucket.audit_worm.id
  role   = aws_iam_role.s3_replication.arn

  rule {
    id     = "replicate-to-eu"
    status = "Enabled"
    destination {
      bucket        = "arn:aws:s3:::governance-platform-audit-worm-prod-eu-west-1"
      storage_class = "STANDARD_IA"
      replication_time {
        status  = "Enabled"
        time { minutes = 15 }
      }
      metrics {
        status = "Enabled"
        event_threshold { minutes = 15 }
      }
    }
  }
}

resource "aws_iam_role" "s3_replication" {
  name = "governance-prod-s3-replication"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "s3.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
  tags = local.common_tags
}

# ── Outputs ───────────────────────────────────────────────────────────────────
output "eks_cluster_name"    { value = module.eks.cluster_name }
output "eks_cluster_endpoint" { value = module.eks.cluster_endpoint }
output "aurora_writer_endpoint" { value = module.aurora.cluster_endpoint }
output "aurora_reader_endpoint" { value = module.aurora.cluster_reader_endpoint }
output "redis_primary_endpoint" { value = module.redis.primary_endpoint }
output "kafka_bootstrap_brokers" { value = module.msk.bootstrap_brokers_tls }
output "audit_worm_bucket"   { value = aws_s3_bucket.audit_worm.bucket }
