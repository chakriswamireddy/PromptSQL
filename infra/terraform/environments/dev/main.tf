terraform {
  required_version = ">= 1.8.0"
  backend "s3" {
    bucket         = "governance-platform-tfstate-dev"
    key            = "core/terraform.tfstate"
    region         = "us-east-1"
    dynamodb_table = "governance-platform-tfstate-lock-dev"
    encrypt        = true
  }
}

provider "aws" {
  region = "us-east-1"
  default_tags {
    tags = {
      Environment = "dev"
      ManagedBy   = "terraform"
      Project     = "governance-platform"
    }
  }
}

module "core" {
  source      = "../../modules/core"
  environment = "dev"
  vpc_cidr    = "10.0.0.0/16"
}
