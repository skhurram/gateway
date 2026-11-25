output "cluster_endpoint" {
  description = "SKS Kubernetes API endpoint."
  value       = module.cluster.cluster_endpoint
}

output "kubeconfig" {
  description = "Short-lived administrator kubeconfig."
  value       = module.cluster.kubeconfig
  sensitive   = true
}

output "gateway_release" {
  description = "Installed gateway release name."
  value       = module.gateway.release_name
}
