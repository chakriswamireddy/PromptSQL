variable "environment" {
  type        = string
  description = "dev | staging | prod"
  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be dev, staging, or prod"
  }
}

variable "aws_region" {
  type    = string
  description = "AWS region for this cluster"
}

variable "cluster_name" {
  type    = string
  description = "EKS cluster name (must be unique per account+region)"
}

variable "kubernetes_version" {
  type    = string
  default = "1.30"
}

variable "vpc_id" {
  type    = string
  description = "VPC ID from the core module"
}

variable "private_subnet_ids" {
  type    = list(string)
  description = "Private subnet IDs for the node groups and control plane ENIs"
}

variable "karpenter_node_role_name" {
  type    = string
  description = "IAM role name attached to Karpenter-managed nodes"
  default = ""
}

variable "node_groups" {
  description = "Map of managed node group configs (used for system/critical pods only; workloads go to Karpenter)"
  type = map(object({
    instance_types = list(string)
    min_size       = number
    max_size       = number
    desired_size   = number
    labels         = map(string)
    taints         = list(object({ key = string, value = string, effect = string }))
  }))
  default = {
    system = {
      instance_types = ["m7i.large"]
      min_size       = 2
      max_size       = 6
      desired_size   = 2
      labels         = { "governance.io/pool" = "system" }
      taints         = []
    }
  }
}

variable "cluster_admin_role_arns" {
  type    = list(string)
  description = "IAM role ARNs granted cluster-admin via aws-auth"
  default = []
}

variable "tags" {
  type    = map(string)
  default = {}
}
