function "project_of" {
  params = [target]
  result = target.base.project_id
}

target "functions" {
  project_id = "vtproject-function"
  target     = "artifact"
  args = {
    PARAMETER = project_of({ base = { project_id = "parameter" } })
    LOOP      = join(",", [for target in [{ base = { project_id = "loop" } }] : target.base.project_id])
  }
}
