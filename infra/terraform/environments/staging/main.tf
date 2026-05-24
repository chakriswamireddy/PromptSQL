terraform {
  required_version = ">= 1.8.0"
  backend "s3" {
    bucket         = "governance-platform-tfstate-staging"
    key            = "core/terraform.tfstate"
    region         = "us-east-1"
    dynamodb_table = "governance-platform-tfstate-lock-staging"
    encrypt        = true
  }
}

provider "aws" {
  region = "us-east-1"
}

module "core" {
  source      = "../../modules/core"
  environment = "staging"
  vpc_cidr    = "10.1.0.0/16"
}
