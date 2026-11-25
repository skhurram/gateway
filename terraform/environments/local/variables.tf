variable "kubeconfig_path" {
  description = "Path to the kubeconfig containing the kind cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "kind kubeconfig context."
  type        = string
  default     = "kind-edge-gateway"
}

variable "namespace" {
  description = "Namespace for the local demo."
  type        = string
  default     = "edge"
}

variable "node_port" {
  description = "Host port mapped to the kind gateway NodePort."
  type        = number
  default     = 8080

  validation {
    condition     = var.node_port >= 1 && var.node_port <= 65535
    error_message = "node_port must be a valid TCP port."
  }
}

variable "helm_timeout_seconds" {
  description = "Seconds Helm waits for gateway readiness."
  type        = number
  default     = 300

  validation {
    condition     = var.helm_timeout_seconds > 0
    error_message = "helm_timeout_seconds must be positive."
  }
}
