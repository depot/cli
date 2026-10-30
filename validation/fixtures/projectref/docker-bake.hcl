target "_project" {
  project_id = "vtproject-ref"
}

# MESSAGE reads the project_id of another target.
target "artifact" {
  target      = "artifact"
  description = "reads the project of _project"
  args        = { MESSAGE = target._project.project_id }
  output      = ["type=local,dest=out"]
}
