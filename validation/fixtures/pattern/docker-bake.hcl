variable "RUN" {
  default = "local"
}

target "mx" {
  matrix = { v = ["a", "b"] }
  name   = "mx-${v}"
  target = "image"
  args   = { MESSAGE = v }
  tags   = ["validation-${RUN}-${v}:latest"]
}

target "other" {
  target = "artifact"
  output = ["type=local,dest=out/other"]
}
