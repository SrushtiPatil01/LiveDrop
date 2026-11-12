output "cluster_name" {
  value       = module.eks.cluster_name
  description = "EKS cluster name."
}

output "database_endpoint" {
  value       = aws_db_instance.database.address
  description = "Private PostgreSQL endpoint."
}

output "redis_endpoint" {
  value       = aws_elasticache_replication_group.redis.primary_endpoint_address
  description = "Private Redis primary endpoint."
}
