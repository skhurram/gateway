mock_provider "helm" {}

run "defaults" {
  command = plan

  variables {
    chart_path = "/workspace/deploy/helm/gateway"
  }

  assert {
    condition     = helm_release.gateway.name == "gateway"
    error_message = "the default Helm release name changed"
  }

  assert {
    condition     = helm_release.gateway.namespace == "edge"
    error_message = "the default namespace changed"
  }

  assert {
    condition     = helm_release.gateway.atomic && helm_release.gateway.cleanup_on_fail
    error_message = "failed installs must roll back cleanly"
  }
}

run "configured" {
  command = plan

  variables {
    chart_path      = "/workspace/deploy/helm/gateway"
    release_name    = "edge-demo"
    namespace       = "gateway-demo"
    timeout_seconds = 180
  }

  assert {
    condition = (
      helm_release.gateway.name == "edge-demo" &&
      helm_release.gateway.namespace == "gateway-demo" &&
      helm_release.gateway.timeout == 180
    )
    error_message = "configured release settings were not applied"
  }
}
