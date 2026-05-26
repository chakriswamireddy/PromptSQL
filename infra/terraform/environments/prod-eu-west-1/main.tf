terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.50" }
  }
  backend "s3" {
    bucket         = "governance-platform-tfstate"
    key            = "prod-eu-west-1/terraform.tfstate"
    region         = "us-east-1"   # remote state bucket lives in primary region
    encrypt        = true
    dynamodb_table = "governance-platform-tfstate-lock"
  }
}

provider "aws" {
  region = "eu-west-1"
  default_tags { tags = local.common_tags }
}

# Secondary Aurora provider — us-east-1 needed to add the secondary to the global cluster
provider "aws" {
  alias  = "primary"
  region = "us-east-1"
}

locals {
  environment  = "prod"
  aws_region   = "eu-west-1"
  cluster_name = "governance-prod-eu-west-1"
  common_tags = {
    Environment = local.environment
    Region      = local.aws_region
    ManagedBy   = "terraform"
    Project     = "governance-platform"
    DataResidency = "eu"
  }
}

module "core" {
  source      = "../../modules/core"
  environment = local.environment
  aws_region  = local.aws_region
  vpc_cidr    = "10.2.0.0/16"
}

module "kms" {
  source      = "../../modules/kms"
  environment = local.environment
}

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

# ── Aurora secondary cluster (read-only replica of the global cluster) ────────
# The global cluster is created in prod-us-east-1; we join it here as a secondary.
data "terraform_remote_state" "primary" {
  backend = "s3"
  config = {
    bucket = "governance-platform-tfstate"
    key    = "prod-us-east-1/terraform.tfstate"
    region = "us-east-1"
  }
}

resource "aws_db_subnet_group" "aurora_secondary" {
  name       = "governance-prod-eu-aurora"
  subnet_ids = module.core.database_subnet_ids
}

resource "aws_security_group" "aurora_secondary" {
  name        = "governance-prod-eu-aurora-sg"
  description = "Aurora secondary — allow EKS nodes"
  vpc_id      = module.core.vpc_id
}

resource "aws_security_group_rule" "aurora_secondary_ingress" {
  type                     = "ingress"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  source_security_group_id = module.eks.node_security_group_id
  security_group_id        = aws_security_group.aurora_secondary.id
}

resource "aws_rds_cluster" "secondary" {
  cluster_identifier        = "governance-prod-eu-aurora-secondary"
  global_cluster_identifier = data.terraform_remote_state.primary.outputs.aurora_global_cluster_id
  engine                    = "aurora-postgresql"
  engine_version            = "16.2"

  db_subnet_group_name   = aws_db_subnet_group.aurora_secondary.name
  vpc_security_group_ids = [aws_security_group.aurora_secondary.id]

  # Secondary clusters are read-only; no master credentials needed.
  skip_final_snapshot = false

  storage_encrypted  = true
  kms_key_id         = module.kms.key_arn
  deletion_protection = true

  tags = local.common_tags
}

resource "aws_rds_cluster_instance" "secondary_reader" {
  cluster_identifier = aws_rds_cluster.secondary.id
  identifier         = "governance-prod-eu-reader"
  instance_class     = "db.serverless"
  engine             = aws_rds_cluster.secondary.engine
  engine_version     = aws_rds_cluster.secondary.engine_version
  publicly_accessible = false
  tags               = local.common_tags
}

# ── ElastiCache Redis (per-region, not replicated — per plan §15.4) ───────────
module "redis" {
  source                    = "../../modules/elasticache"
  environment               = local.environment
  vpc_id                    = module.core.vpc_id
  subnet_ids                = module.core.private_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn               = module.kms.key_arn
  node_type                 = "cache.r7g.large"
  num_cache_clusters        = 3
}

# ── MSK Kafka (secondary — receives from primary via MirrorMaker 2) ───────────
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
}

# ── S3 WORM bucket (secondary for EU data residency) ─────────────────────────
resource "aws_s3_bucket" "audit_worm" {
  bucket = "governance-platform-audit-worm-prod-eu-west-1"
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

# ── Outputs ───────────────────────────────────────────────────────────────────
output "eks_cluster_name"         { value = module.eks.cluster_name }
output "aurora_secondary_endpoint" { value = aws_rds_cluster.secondary.reader_endpoint }
output "redis_primary_endpoint"   { value = module.redis.primary_endpoint }
output "kafka_bootstrap_brokers"  { value = module.msk.bootstrap_brokers_tls }
