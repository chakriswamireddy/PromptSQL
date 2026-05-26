output "cluster_endpoint" {
  description = "Writer endpoint"
  value       = aws_rds_cluster.primary.endpoint
}

output "cluster_reader_endpoint" {
  description = "Reader endpoint (load-balanced across all read replicas)"
  value       = aws_rds_cluster.primary.reader_endpoint
}

output "cluster_identifier" {
  value = aws_rds_cluster.primary.cluster_identifier
}

output "global_cluster_identifier" {
  value = aws_rds_global_cluster.this.id
}

output "db_name" {
  value = aws_rds_cluster.primary.database_name
}

output "security_group_id" {
  value = aws_security_group.aurora.id
}

output "master_secret_arn" {
  value     = aws_secretsmanager_secret.db_master.arn
  sensitive = true
}
