target "_project" {
  project_id = "vtproject-ref"
  call       = "outline"
}

target "described" {
  target      = "artifact"
  description = "built in ${target._project.project_id} with ${target._project.call}"
}
