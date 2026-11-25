variable "cluster_name" {
  description = "SKS cluster name."
  type        = string
  default     = "edge-gateway-demo"
}

variable "zone" {
  description = "Exoscale zone."
  type        = string
  default     = "ch-gva-2"
}

variable "owner" {
  description = "Owner label applied to the cluster."
  type        = string
  default     = "gateway-demo"
}

variable "node_instance_type" {
  description = "Exoscale worker instance type."
  type        = string
  default     = "standard.medium"
}

variable "node_count" {
  description = "Number of worker nodes."
  type        = number
  default     = 1

  validation {
    condition     = var.node_count >= 1 && var.node_count <= 3
    error_message = "node_count must be between 1 and 3 for this demo."
  }
}

variable "node_disk_size" {
  description = "Worker disk size in GiB."
  type        = number
  default     = 50

  validation {
    condition     = var.node_disk_size >= 50
    error_message = "node_disk_size must be at least 50 GiB."
  }
}

variable "kubeconfig_ttl_seconds" {
  description = "Kubeconfig lifetime in seconds."
  type        = number
  default     = 3600

  validation {
    condition     = var.kubeconfig_ttl_seconds >= 600
    error_message = "kubeconfig_ttl_seconds must be at least 600."
  }
}

variable "kubeconfig_early_renewal_seconds" {
  description = "Seconds before expiry to renew the kubeconfig."
  type        = number
  default     = 300

  validation {
    condition     = var.kubeconfig_early_renewal_seconds >= 0
    error_message = "kubeconfig_early_renewal_seconds cannot be negative."
  }
}

variable "create_timeout" {
  description = "Timeout for cluster and node pool creation."
  type        = string
  default     = "30m"
}

variable "delete_timeout" {
  description = "Timeout for cluster and node pool deletion."
  type        = string
  default     = "30m"
}
