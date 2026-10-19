target "_project" {
  project_id = "vtproject-ref"
  call       = "outline"
  network    = "host"
  target     = "artifact"
}

target "described" {
  target      = "artifact"
  description = "${target._project.call} ${target._project.network} ${target._project.target} [${target._project.project_id}]"
}
