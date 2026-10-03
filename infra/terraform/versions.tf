terraform {
  required_version = ">= 1.16.5, < 2.0.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}
provider "aws" {
  region = "us-east-2"
  default_tags { tags = { Project = "LabRelay", Environment = "pilot" } }
}
