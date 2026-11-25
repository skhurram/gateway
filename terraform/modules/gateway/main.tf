resource "helm_release" "gateway" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true

  chart = var.chart_path
  values = concat(
    [yamlencode(var.values)],
    [for path in var.values_files : file(path)],
  )

  atomic          = true
  cleanup_on_fail = true
  wait            = true
  wait_for_jobs   = true
  timeout         = var.timeout_seconds
}
