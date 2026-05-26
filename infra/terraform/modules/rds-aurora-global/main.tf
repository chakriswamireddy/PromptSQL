terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.50"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

locals {
  name_prefix = "governance-${var.environment}"
  common_tags = merge(var.tags, {
    Environment = var.environment
    ManagedBy   = "terraform"
    Project     = "governance-platform"
  })
}

# ── Subnet group ──────────────────────────────────────────────────────────────
resource "aws_db_subnet_group" "this" {
  name        = "${local.name_prefix}-aurora"
  subnet_ids  = var.db_subnet_ids
  description = "Aurora PostgreSQL subnet group — private subnets only"
  tags        = local.common_tags
}

# ── Security group ────────────────────────────────────────────────────────────
resource "aws_security_group" "aurora" {
  name        = "${local.name_prefix}-aurora-sg"
  description = "Aurora PostgreSQL — allow PgBouncer / EKS nodes on 5432"
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${local.name_prefix}-aurora-sg" })
}

resource "aws_security_group_rule" "aurora_ingress" {
  for_each = toset(var.allowed_security_group_ids)

  type                     = "ingress"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  source_security_group_id = each.value
  security_group_id        = aws_security_group.aurora.id
  description              = "PostgreSQL from ${each.value}"
}

resource "aws_security_group_rule" "aurora_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  security_group_id = aws_security_group.aurora.id
}

# ── Cluster parameter group ────────────────────────────────────────────────────
resource "aws_rds_cluster_parameter_group" "this" {
  name        = "${local.name_prefix}-aurora-pg16"
  family      = "aurora-postgresql16"
  description = "Governance Platform — Aurora PostgreSQL 16 cluster params"

  parameter {
    name  = "wal_level"
    value = "logical"  # required for cross-region logical replication
  }

  parameter {
    name  = "max_wal_senders"
    value = "20"
  }

  parameter {
    name  = "max_replication_slots"
    value = "20"
  }

  parameter {
    name  = "log_connections"
    value = "1"
  }

  parameter {
    name  = "log_disconnections"
    value = "1"
  }

  parameter {
    name  = "log_lock_waits"
    value = "1"
  }

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }

  tags = local.common_tags
}

# ── Master password in Secrets Manager ────────────────────────────────────────
resource "random_password" "master" {
  length           = 32
  special          = true
  override_special = "!#$%&*()-_=+[]{}|;:,.<>?"
}

resource "aws_secretsmanager_secret" "db_master" {
  name                    = "${local.name_prefix}/aurora/master-password"
  recovery_window_in_days = 30
  kms_key_id              = var.kms_key_arn
  tags                    = local.common_tags
}

resource "aws_secretsmanager_secret_version" "db_master" {
  secret_id = aws_secretsmanager_secret.db_master.id
  secret_string = jsonencode({
    username = "governance_master"
    password = random_password.master.result
  })
}

# ── Aurora Global Cluster (shell, no engine) ──────────────────────────────────
resource "aws_rds_global_cluster" "this" {
  global_cluster_identifier = "${local.name_prefix}-global"
  engine                    = "aurora-postgresql"
  engine_version            = var.engine_version
  database_name             = var.db_name
  deletion_protection       = var.deletion_protection
  storage_encrypted         = true
}

# ── Primary regional cluster ──────────────────────────────────────────────────
resource "aws_rds_cluster" "primary" {
  cluster_identifier        = "${local.name_prefix}-aurora-primary"
  global_cluster_identifier = aws_rds_global_cluster.this.id
  engine                    = "aurora-postgresql"
  engine_version            = var.engine_version
  database_name             = var.db_name
  master_username           = jsondecode(aws_secretsmanager_secret_version.db_master.secret_string)["username"]
  master_password           = jsondecode(aws_secretsmanager_secret_version.db_master.secret_string)["password"]

  db_subnet_group_name            = aws_db_subnet_group.this.name
  vpc_security_group_ids          = [aws_security_group.aurora.id]
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.this.name

  backup_retention_period      = var.backup_retention_days
  preferred_backup_window      = "03:00-04:00"
  preferred_maintenance_window = "sun:04:30-sun:06:00"

  storage_encrypted  = true
  kms_key_id         = var.kms_key_arn
  deletion_protection = var.deletion_protection

  enabled_cloudwatch_logs_exports = ["postgresql"]

  serverlessv2_scaling_configuration {
    min_capacity = var.min_capacity
    max_capacity = var.max_capacity
  }

  skip_final_snapshot = var.environment != "prod"
  final_snapshot_identifier = var.environment == "prod" ? "${local.name_prefix}-aurora-final-${formatdate("YYYY-MM-DD", timestamp())}" : null

  tags = local.common_tags

  lifecycle {
    ignore_changes = [master_password]  # managed by rotation
  }
}

# ── Primary writer instance (Serverless v2) ────────────────────────────────────
resource "aws_rds_cluster_instance" "writer" {
  cluster_identifier  = aws_rds_cluster.primary.id
  identifier          = "${local.name_prefix}-aurora-writer"
  instance_class      = "db.serverless"
  engine              = aws_rds_cluster.primary.engine
  engine_version      = aws_rds_cluster.primary.engine_version
  publicly_accessible = false
  tags                = local.common_tags
}

# ── Read replica instance ──────────────────────────────────────────────────────
resource "aws_rds_cluster_instance" "reader" {
  cluster_identifier  = aws_rds_cluster.primary.id
  identifier          = "${local.name_prefix}-aurora-reader"
  instance_class      = "db.serverless"
  engine              = aws_rds_cluster.primary.engine
  engine_version      = aws_rds_cluster.primary.engine_version
  publicly_accessible = false
  tags                = local.common_tags
}

# ── Enhanced monitoring role ───────────────────────────────────────────────────
resource "aws_iam_role" "rds_monitoring" {
  name = "${local.name_prefix}-rds-monitoring"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "monitoring.rds.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy_attachment" "rds_monitoring" {
  role       = aws_iam_role.rds_monitoring.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"
}
