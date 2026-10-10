target "_project" {
  project_id = "vtproject-ref"
}

target "_noproject" {
}

target "references" {
  target      = "artifact"
  description = "built in ${target._project.project_id}"
  args = {
    INDEX = target._project["project_id"]
    EMPTY = "none:${target._noproject.project_id}"
  }
}
