variable "aws_region" {
  type        = string
  description = "AWS region for the LiveDrop cluster and managed data services."
  default     = "us-east-1"
}

variable "environment" {
  type        = string
  description = "Environment name used in resource names."
  default     = "livedrop"
}

variable "database_password" {
  type        = string
  description = "Password for the managed PostgreSQL instance; provide through a secret manager."
  sensitive   = true
}

variable "redis_auth_token" {
  type        = string
  description = "ElastiCache AUTH token; provide through a secret manager."
  sensitive   = true

  validation {
    condition     = length(var.redis_auth_token) >= 16 && length(var.redis_auth_token) <= 128
    error_message = "ElastiCache AUTH tokens must be between 16 and 128 characters."
  }
}

variable "vpc_cidr" {
  type        = string
  description = "CIDR block for the workload VPC."
  default     = "10.40.0.0/16"

  validation {
    condition     = can(cidrsubnet(var.vpc_cidr, 4, 5))
    error_message = "The VPC CIDR must have room for six /20 or smaller subnets."
  }
}
