target "_common" {
  project_id = "vtproject-common"
  labels     = { team = "build" }
}

target "copied" {
  target = "artifact"
  labels = merge(target._common.labels, { role = "copy" })
}

target "inherited" {
  inherits = ["_common"]
  target   = "artifact"
  labels   = { role = "child" }
}

target "both" {
  project_id = "vtproject-both"
  target     = "artifact"
  labels     = { role = "both" }
  args = {
    COMMON = target._common.project_id
    KEYS   = join(",", keys(target._common.labels))
    WHOLE  = join(",", keys(lookup(target._common, "labels", {})))
  }
}

group "labels" {
  targets = ["copied", "inherited", "both"]
}
