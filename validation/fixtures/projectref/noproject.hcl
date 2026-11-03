variable "ATTRIBUTE" {
  default = "project_id"
}

target "_base" {
}

target "noproject" {
  target = "artifact"
  args = {
    LOOKUP  = "none:${lookup(target._base, "project_id", "fallback")}"
    DYNAMIC = "none:${target._base[ATTRIBUTE]}"
  }
}
