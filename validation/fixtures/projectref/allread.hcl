target "_project" {
  project_id = "vtproject-ref"
  call       = "outline"
  network    = "host"
  target     = "artifact"
}

# The description reads call, network, and target, so bake --print cannot
# use any of them to carry project_id.
target "described" {
  target      = "artifact"
  description = "${target._project.call} ${target._project.network} ${target._project.target} [${target._project.project_id}]"
}
