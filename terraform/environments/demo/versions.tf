terraform {
  required_version = ">= 1.10.0"

  backend "s3" {
    key            = "edge-gateway/demo.tfstate"
    use_lockfile   = true
    use_path_style = true

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
  }

  required_providers {
    exoscale = {
      source  = "exoscale/exoscale"
      version = ">= 0.65.0, < 1.0.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.0"
    }
  }
}
