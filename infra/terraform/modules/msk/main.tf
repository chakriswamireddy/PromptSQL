terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.50" }
  }
}

variable "environment" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string); description = "One subnet per AZ (3 AZs recommended)" }
variable "allowed_security_group_ids" { type = list(string) }
variable "kms_key_arn" { type = string }
variable "broker_instance_type" { type = string; default = "kafka.m7g.large" }
variable "number_of_broker_nodes" { type = number; default = 3 }
variable "kafka_version" { type = string; default = "3.7.x" }
variable "volume_size_gib" { type = number; default = 1000 }
variable "tags" { type = map(string); default = {} }

locals {
  name_prefix = "governance-${var.environment}"
  common_tags = merge(var.tags, { Environment = var.environment, ManagedBy = "terraform" })
}

resource "aws_security_group" "msk" {
  name        = "${local.name_prefix}-msk-sg"
  description = "MSK Kafka — allow EKS nodes on 9098 (mTLS)"
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${local.name_prefix}-msk-sg" })
}

resource "aws_security_group_rule" "msk_ingress" {
  for_each                 = toset(var.allowed_security_group_ids)
  type                     = "ingress"
  from_port                = 9098
  to_port                  = 9098
  protocol                 = "tcp"
  source_security_group_id = each.value
  security_group_id        = aws_security_group.msk.id
  description              = "MSK mTLS broker port from ${each.value}"
}

resource "aws_security_group_rule" "msk_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  security_group_id = aws_security_group.msk.id
}

resource "aws_msk_configuration" "this" {
  name           = "${local.name_prefix}-kafka-config"
  kafka_versions = [var.kafka_version]
  server_properties = <<-EOF
    auto.create.topics.enable=false
    default.replication.factor=3
    min.insync.replicas=2
    num.partitions=6
    log.retention.hours=168
    log.segment.bytes=1073741824
    compression.type=zstd
    message.max.bytes=10485760
    replica.fetch.max.bytes=10485760
    offsets.topic.replication.factor=3
    transaction.state.log.replication.factor=3
    transaction.state.log.min.isr=2
  EOF
}

resource "aws_msk_cluster" "this" {
  cluster_name           = "${local.name_prefix}-kafka"
  kafka_version          = var.kafka_version
  number_of_broker_nodes = var.number_of_broker_nodes

  broker_node_group_info {
    instance_type  = var.broker_instance_type
    client_subnets = var.subnet_ids
    storage_info {
      ebs_storage_info {
        volume_size = var.volume_size_gib
      }
    }
    security_groups = [aws_security_group.msk.id]
  }

  configuration_info {
    arn      = aws_msk_configuration.this.arn
    revision = aws_msk_configuration.this.latest_revision
  }

  # mTLS only — IAM auth disabled (cert-based identity for all producers/consumers)
  client_authentication {
    tls {
      certificate_authority_arns = []  # use ACM PCA if needed; leave empty to use mutual TLS with self-managed certs
    }
    sasl {
      iam   = false
      scram = false
    }
  }

  encryption_info {
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
    encryption_at_rest_kms_key_arn = var.kms_key_arn
  }

  logging_info {
    broker_logs {
      cloudwatch_logs {
        enabled   = true
        log_group = "/aws/msk/${local.name_prefix}"
      }
    }
  }

  open_monitoring {
    prometheus {
      jmx_exporter  { enabled_in_broker = true }
      node_exporter { enabled_in_broker = true }
    }
  }

  tags = local.common_tags
}

resource "aws_cloudwatch_log_group" "msk" {
  name              = "/aws/msk/${local.name_prefix}"
  retention_in_days = 90
  tags              = local.common_tags
}

output "bootstrap_brokers_tls" {
  value = aws_msk_cluster.this.bootstrap_brokers_tls
}

output "zookeeper_connect_string" {
  value = aws_msk_cluster.this.zookeeper_connect_string
}

output "cluster_arn" {
  value = aws_msk_cluster.this.arn
}

output "security_group_id" {
  value = aws_security_group.msk.id
}
