variable "chart_path" {
  description = "Path to the local gateway Helm chart."
  type        = string

  validation {
    condition     = trimspace(var.chart_path) != ""
    error_message = "chart_path must not be empty."
  }
}

variable "release_name" {
  description = "Helm release name."
  type        = string
  default     = "gateway"

  validation {
    condition     = can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", var.release_name))
    error_message = "release_name must be a valid lowercase Helm release name."
  }
}

variable "namespace" {
  description = "Kubernetes namespace for the release."
  type        = string
  default     = "edge"

  validation {
    condition     = trimspace(var.namespace) != ""
    error_message = "namespace must not be empty."
  }
}

variable "timeout_seconds" {
  description = "Seconds Helm waits for the release to become ready."
  type        = number
  default     = 300

  validation {
    condition     = var.timeout_seconds >= 1
    error_message = "timeout_seconds must be positive."
  }
}

variable "values" {
  description = "Helm values rendered as the final inline values layer."
  type        = any
  default     = {}
}

variable "values_files" {
  description = "Additional YAML values files, in merge order."
  type        = list(string)
  default     = []
}
