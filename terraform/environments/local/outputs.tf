output "gateway_url" {
  description = "Local kind NodePort URL."
  value       = "http://localhost:${var.node_port}/api/users/hello"
}

output "release_status" {
  description = "Gateway Helm release status."
  value       = module.gateway.status
}
