terraform {
  required_version = ">= 1.7.0"

  required_providers {
    exoscale = {
      source  = "exoscale/exoscale"
      version = ">= 0.65.0, < 1.0.0"
    }
  }
}
