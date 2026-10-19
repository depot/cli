target "_newline" {
  project_id = "\nvtproject-newline"
}

target "_first" {
  project_id = "vtproject-first"
}

target "_second" {
  project_id = "vtproject-second"
}

target "policies" {
  project_id = trimspace(target._newline.project_id)
  target     = "artifact"
  args = {
    SAME = "${{ for key, value in target._first : key => value if key != "name" && key != "project_id" } == { for key, value in target._second : key => value if key != "name" && key != "project_id" }}"
  }
}
