target "_labeled" {
  labels     = { project = "vtproject-labeled" }
  project_id = target._labeled.labels.project
}

target "_mutual" {
  project_id = "vtproject-mutual"
  labels     = { reader = target.cycles.project_id }
}

target "cycles" {
  project_id = target._mutual.project_id
  target     = "artifact"
  args = {
    LABELED = target._labeled.project_id
  }
}
