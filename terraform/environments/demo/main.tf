locals {
  root_dir   = abspath("${path.module}/../../..")
  kubeconfig = yamldecode(module.cluster.kubeconfig)
  cluster    = local.kubeconfig.clusters[0].cluster
  user       = local.kubeconfig.users[0].user
}

module "cluster" {
  source = "../../modules/exoscale-cluster"

  cluster_name       = var.cluster_name
  zone               = var.zone
  node_count         = var.node_count
  node_instance_type = var.node_instance_type
}

module "gateway" {
  source = "../../modules/gateway"

  chart_path = "${local.root_dir}/deploy/helm/gateway"
  namespace  = var.namespace
  values = {
    image = {
      repository = var.gateway_image_repository
      tag        = var.image_tag
      pullPolicy = "IfNotPresent"
    }
    controller = {
      image = {
        repository = var.controller_image_repository
        tag        = var.image_tag
        pullPolicy = "IfNotPresent"
      }
    }
    service = {
      type = "ClusterIP"
      port = 8080
    }
    config = {
      listen = ":8080"
      health = {
        path     = "/health"
        interval = "5s"
        timeout  = "2s"
      }
      circuit_breaker = {
        failure_threshold  = 5
        open_timeout       = "30s"
        half_open_requests = 1
      }
      routes = [
        {
          prefix    = "/"
          upstreams = [var.demo_upstream_url]
        }
      ]
    }
  }

  depends_on = [module.cluster]
}
