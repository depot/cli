variable "SECOND_PROJECT" {
  default = "vtproject-second"
}

group "default" {
  targets = ["first", "second", "third"]
}

target "common" {
  target = "artifact"
}

target "first" {
  inherits   = ["common"]
  output     = ["type=local,dest=out/first"]
  args       = { MESSAGE = "first" }
}

target "second" {
  inherits   = ["common"]
  project_id = SECOND_PROJECT
  output     = ["type=local,dest=out/second"]
  args       = { MESSAGE = "second" }
}

target "third" {
  inherits    = ["second"]
  description = "inherits the project of second"
  output      = ["type=local,dest=out/third"]
  args        = { MESSAGE = "third" }
}
