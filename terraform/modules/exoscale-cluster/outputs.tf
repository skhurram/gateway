output "cluster_id" {
  description = "Exoscale SKS cluster ID."
  value       = exoscale_sks_cluster.this.id
}

output "cluster_endpoint" {
  description = "Kubernetes API endpoint."
  value       = exoscale_sks_cluster.this.endpoint
}

output "nodepool_id" {
  description = "Worker node pool ID."
  value       = exoscale_sks_nodepool.workers.id
}

output "kubeconfig" {
  description = "Short-lived administrator kubeconfig."
  value       = exoscale_sks_kubeconfig.admin.kubeconfig
  sensitive   = true
}
