locals {
  root_dir = abspath("${path.module}/../../..")
  demo_manifests = toset(flatten([
    for path in [
      "${local.root_dir}/deploy/kind/echo.yaml",
      "${local.root_dir}/deploy/observability.yaml",
      ] : [
      for index, document in compact(split("\n---\n", file(path))) : "${path}#${index}"
    ]
  ]))
}

resource "kubernetes_namespace_v1" "edge" {
  metadata {
    name = var.namespace
  }
}

resource "kubernetes_manifest" "demo" {
  for_each = {
    for ref in local.demo_manifests :
    ref => yamldecode(split("#", ref)[1] == "0" ?
      split("\n---\n", file(split("#", ref)[0]))[0] :
      split("\n---\n", file(split("#", ref)[0]))[tonumber(split("#", ref)[1])]
    )
  }

  manifest = merge(each.value, {
    metadata = merge(each.value.metadata, {
      namespace = var.namespace
    })
  })

  depends_on = [kubernetes_namespace_v1.edge]
}

module "gateway" {
  source = "../../modules/gateway"

  chart_path      = "${local.root_dir}/deploy/helm/gateway"
  namespace       = var.namespace
  timeout_seconds = var.helm_timeout_seconds
  values_files    = ["${local.root_dir}/deploy/kind/gateway-values.yaml"]

  depends_on = [kubernetes_manifest.demo]
}
