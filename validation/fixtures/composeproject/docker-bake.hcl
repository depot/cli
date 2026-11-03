# fromcompose inherits the project of the compose service api.
target "fromcompose" {
  inherits = ["api"]
  args     = { MESSAGE = "fromcompose" }
  output   = ["type=local,dest=out/fromcompose"]
}

target "api" {
  output = ["type=local,dest=out/api"]
}

# The project_id of an HCL target overrides the project of the compose
# service with the same name.
target "worker" {
  project_id = "vtproject-hcl"
  output     = ["type=local,dest=out/worker"]
}
