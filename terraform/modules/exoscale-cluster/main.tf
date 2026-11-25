resource "exoscale_sks_cluster" "this" {
  name                          = var.cluster_name
  zone                          = var.zone
  service_level                 = "starter"
  auto_upgrade                  = true
  cni                           = "calico"
  metrics_server                = true
  create_default_security_group = true

  labels = {
    project = "edge-gateway"
    owner   = var.owner
  }

  timeouts {
    create = var.create_timeout
    delete = var.delete_timeout
  }
}

resource "exoscale_sks_nodepool" "workers" {
  cluster_id = exoscale_sks_cluster.this.id
  zone       = exoscale_sks_cluster.this.zone
  name       = "${var.cluster_name}-workers"

  instance_type = var.node_instance_type
  size          = var.node_count
  disk_size     = var.node_disk_size

  security_group_ids = [exoscale_sks_cluster.this.default_security_group_id]

  timeouts {
    create = var.create_timeout
    delete = var.delete_timeout
  }
}

resource "exoscale_sks_kubeconfig" "admin" {
  cluster_id = exoscale_sks_cluster.this.id
  zone       = exoscale_sks_cluster.this.zone

  user                  = "terraform-admin"
  groups                = ["system:masters"]
  ttl_seconds           = var.kubeconfig_ttl_seconds
  early_renewal_seconds = var.kubeconfig_early_renewal_seconds
}
