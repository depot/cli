target "_base" {
  project_id = "vtproject-base"
}

target "policyread" {
  target = "artifact"
  args = {
    COUNT = length(target._base.policy)
  }
}
