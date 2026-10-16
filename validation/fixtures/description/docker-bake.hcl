target "_base" {
  description = "described by _base"
}

# MESSAGE reads the description of another target. No target sets project_id.
target "artifact" {
  target      = "artifact"
  description = "reads the description of _base"
  args        = { MESSAGE = target._base.description }
  output      = ["type=local,dest=out"]
}
