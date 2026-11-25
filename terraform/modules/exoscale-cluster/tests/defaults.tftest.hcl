mock_provider "exoscale" {}

run "small_demo_defaults" {
  command = plan

  assert {
    condition = (
      exoscale_sks_cluster.this.service_level == "starter" &&
      exoscale_sks_nodepool.workers.size == 1
    )
    error_message = "the cluster defaults are not sized for a short demo"
  }

  assert {
    condition = (
      exoscale_sks_kubeconfig.admin.user == "terraform-admin" &&
      exoscale_sks_kubeconfig.admin.ttl_seconds == 3600
    )
    error_message = "the short-lived administrator kubeconfig changed"
  }
}
