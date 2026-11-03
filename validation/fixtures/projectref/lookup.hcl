target "_project" {
  project_id = "vtproject-lookup"
}

target "_noproject" {
}

target "lookup" {
  project_id = lookup(target._project, "project_id", "vtproject-default")
  target     = "artifact"
  args = {
    EMPTY = "none:${lookup(target._noproject, "project_id", "default")}"
    KEYS  = join(",", [for key in keys(target._project) : key if key == "project_id"])
  }
}
