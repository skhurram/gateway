provider "exoscale" {
  # EXOSCALE_API_KEY and EXOSCALE_API_SECRET are read from the environment.
}

provider "helm" {
  kubernetes = {
    host                   = local.cluster.server
    cluster_ca_certificate = base64decode(local.cluster.certificate-authority-data)
    client_certificate     = base64decode(local.user.client-certificate-data)
    client_key             = base64decode(local.user.client-key-data)
  }
}
