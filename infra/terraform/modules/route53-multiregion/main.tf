terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.50" }
  }
}

variable "hosted_zone_id" {
  type        = string
  description = "Route53 hosted zone ID for governance-platform.io"
}

variable "api_domain" {
  type        = string
  description = "e.g. api.governance-platform.io"
}

variable "regions" {
  description = "Map of region → ALB DNS name + health check path"
  type = map(object({
    alb_dns_name      : string
    alb_zone_id       : string
    health_check_path : string
  }))
}

variable "failover_ttl" {
  type    = number
  default = 30
  description = "TTL in seconds for failover DNS records"
}

variable "tags" { type = map(string); default = {} }

# ── Health checks ─────────────────────────────────────────────────────────────
resource "aws_route53_health_check" "region" {
  for_each = var.regions

  fqdn              = each.value.alb_dns_name
  port              = 443
  type              = "HTTPS"
  resource_path     = each.value.health_check_path
  failure_threshold = 3
  request_interval  = 10

  tags = merge(var.tags, { Region = each.key })
}

# ── Latency-based routing records (active-active reads) ───────────────────────
resource "aws_route53_record" "api_latency" {
  for_each = var.regions

  zone_id        = var.hosted_zone_id
  name           = var.api_domain
  type           = "A"
  set_identifier = each.key

  alias {
    name                   = each.value.alb_dns_name
    zone_id                = each.value.alb_zone_id
    evaluate_target_health = true
  }

  latency_routing_policy {
    region = each.key
  }

  health_check_id = aws_route53_health_check.region[each.key].id
}

# ── Write-endpoint record (single-writer, pinned to primary) ──────────────────
# This record always points to the primary region's ALB.
# Failover is manual: update primary_region variable + apply + confirm.
variable "primary_region" {
  type = string
}

resource "aws_route53_record" "api_write" {
  zone_id = var.hosted_zone_id
  name    = "write.${var.api_domain}"
  type    = "A"
  ttl     = var.failover_ttl

  alias {
    name                   = var.regions[var.primary_region].alb_dns_name
    zone_id                = var.regions[var.primary_region].alb_zone_id
    evaluate_target_health = true
  }
}

output "api_latency_record_fqdn" {
  value = var.api_domain
}

output "api_write_record_fqdn" {
  value = "write.${var.api_domain}"
}

output "health_check_ids" {
  value = { for k, v in aws_route53_health_check.region : k => v.id }
}
