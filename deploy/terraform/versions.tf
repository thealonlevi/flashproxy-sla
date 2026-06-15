terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

# Credentials are NEVER set here. Provide them at apply time via env vars
# (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) or `profile = ...` / AWS SSO.
provider "aws" {
  alias  = "ashburn" # Ashburn, Virginia
  region = "us-east-1"
}

provider "aws" {
  alias  = "frankfurt"
  region = "eu-central-1"
}
