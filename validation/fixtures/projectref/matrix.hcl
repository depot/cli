target "matrix" {
  name       = "matrix-${replace(target.base.project_id, "_", "-")}"
  matrix     = { target = [{ base = { project_id = "from_matrix" } }] }
  project_id = "vtproject-matrix"
  target     = "artifact"
  args = {
    VALUE = lookup(target.base, "project_id", "fallback")
  }
}
