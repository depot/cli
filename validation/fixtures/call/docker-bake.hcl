target "outline" {
  call = "outline"
}

target "targets" {
  call = "targets"
}

target "check" {
  call = "check"
}

target "check-builtin" {
  dockerfile = "Builtin.Dockerfile"
  call       = "check"
}

target "build" {
  call   = "build"
  target = "artifact"
  output = ["type=local,dest=out"]
}

target "check-json" {
  call = "check,format=json"
}

target "check-ignorestatus" {
  call = "check,ignorestatus=true"
}
