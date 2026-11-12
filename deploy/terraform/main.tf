data "aws_availability_zones" "available" {
  state = "available"
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.13.0"

  name = var.environment
  cidr = var.vpc_cidr

  azs             = slice(data.aws_availability_zones.available.names, 0, 3)
  private_subnets = [
    cidrsubnet(var.vpc_cidr, 4, 0),
    cidrsubnet(var.vpc_cidr, 4, 1),
    cidrsubnet(var.vpc_cidr, 4, 2),
  ]
  public_subnets = [
    cidrsubnet(var.vpc_cidr, 4, 3),
    cidrsubnet(var.vpc_cidr, 4, 4),
    cidrsubnet(var.vpc_cidr, 4, 5),
  ]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true
  enable_dns_support   = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "20.31.6"

  cluster_name                             = var.environment
  cluster_version                          = "1.34"
  cluster_endpoint_public_access           = false
  cluster_endpoint_private_access          = true
  enable_cluster_creator_admin_permissions = true
  vpc_id                                   = module.vpc.vpc_id
  subnet_ids                               = module.vpc.private_subnets

  eks_managed_node_groups = {
    services = {
      instance_types = ["m6i.large"]
      min_size       = 2
      max_size       = 10
      desired_size   = 3
    }
  }
}

resource "aws_security_group" "database" {
  name        = "${var.environment}-postgres"
  description = "PostgreSQL access from the EKS cluster"
  vpc_id      = module.vpc.vpc_id

  ingress {
    description     = "Postgres from EKS nodes"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [module.eks.node_security_group_id]
  }
}

resource "aws_db_subnet_group" "database" {
  name       = "${var.environment}-postgres"
  subnet_ids = module.vpc.private_subnets
}

resource "aws_db_instance" "database" {
  identifier               = "${var.environment}-postgres"
  engine                   = "postgres"
  engine_version           = "16.4"
  instance_class           = "db.t4g.medium"
  allocated_storage        = 50
  max_allocated_storage    = 200
  db_name                  = "livedrop"
  username                 = "livedrop"
  password                 = var.database_password
  db_subnet_group_name     = aws_db_subnet_group.database.name
  vpc_security_group_ids   = [aws_security_group.database.id]
  publicly_accessible      = false
  storage_encrypted        = true
  backup_retention_period  = 7
  deletion_protection      = true
  skip_final_snapshot      = false
  final_snapshot_identifier = "${var.environment}-postgres-final"
}

resource "aws_security_group" "redis" {
  name        = "${var.environment}-redis"
  description = "Redis access from the EKS nodes"
  vpc_id      = module.vpc.vpc_id

  ingress {
    description     = "Redis from EKS nodes"
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = [module.eks.node_security_group_id]
  }
}

resource "aws_elasticache_subnet_group" "redis" {
  name       = "${var.environment}-redis"
  subnet_ids = module.vpc.private_subnets
}

resource "aws_elasticache_replication_group" "redis" {
  replication_group_id       = "${var.environment}-redis"
  description                = "LiveDrop inventory cache and Pub/Sub"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = "cache.t4g.small"
  num_cache_clusters         = 2
  automatic_failover_enabled = true
  multi_az_enabled           = true
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  auth_token                 = var.redis_auth_token
  auth_token_update_strategy = "ROTATE"
  subnet_group_name          = aws_elasticache_subnet_group.redis.name
  security_group_ids         = [aws_security_group.redis.id]
}
