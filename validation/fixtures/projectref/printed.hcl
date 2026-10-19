target "_base" {
  project_id = "vtproject-base"
  network    = "host"
}
target "printed" {
  target = "artifact"
  description = "built in ${target._base.project_id} with ${lookup(target._base, "network", "none")}"
}
