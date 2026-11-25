variable "zone" {
  description = "Exoscale zone."
  type        = string
  default     = "ch-gva-2"
}

variable "cluster_name" {
  description = "Short-lived SKS cluster name."
  type        = string
  default     = "edge-gateway-demo"
}

variable "node_count" {
  description = "Worker node count."
  type        = number
  default     = 1
}

variable "node_instance_type" {
  description = "Worker instance type."
  type        = string
  default     = "standard.medium"
}

variable "namespace" {
  description = "Gateway namespace."
  type        = string
  default     = "edge"
}

variable "gateway_image_repository" {
  description = "Registry path for the gateway image."
  type        = string
}

variable "controller_image_repository" {
  description = "Registry path for the controller image."
  type        = string
}

variable "image_tag" {
  description = "Image tag for gateway and controller."
  type        = string
  default     = "v0.4.0"
}

variable "demo_upstream_url" {
  description = "Public or cluster-local HTTP(S) demo upstream."
  type        = string
  default     = "https://httpbin.org"
}
