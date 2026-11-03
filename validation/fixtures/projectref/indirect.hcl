UNUSED = can(target.base.call)
FIELD  = "network"

target "base" {
  project_id = "p"
  network    = "host"
}
target "indirect" {
  target      = "artifact"
  description = target.base[FIELD]
}
