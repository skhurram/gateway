output "release_name" {
  description = "Installed Helm release name."
  value       = helm_release.gateway.name
}

output "namespace" {
  description = "Namespace containing the gateway."
  value       = helm_release.gateway.namespace
}

output "status" {
  description = "Current Helm release status."
  value       = helm_release.gateway.status
}
