target "base" {
  target = "base"
  args   = { MESSAGE = "linked" }
}

target "child" {
  target   = "artifact"
  contexts = { base = "target:base" }
  output   = ["type=local,dest=out/child"]
}
