terraform {
  required_version = ">= 1.8.0"
  backend "s3" {
    bucket         = "governance-platform-tfstate-prod"
    key            = "core/terraform.tfstate"
    region         = "us-east-1"
    dynamodb_table = "governance-platform-tfstate-lock-prod"
    encrypt        = true
  }
}

provider "aws" {
  region = "us-east-1"
}

module "core" {
  source      = "../../modules/core"
  environment = "prod"
  vpc_cidr    = "10.2.0.0/16"
}
