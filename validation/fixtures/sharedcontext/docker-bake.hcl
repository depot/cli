group "default" {
  targets = ["a", "b"]
}

target "a" {
  dockerfile = "Dockerfile.a"
  output     = ["type=local,dest=out/a"]
}

target "b" {
  dockerfile = "Dockerfile.b"
  output     = ["type=local,dest=out/b"]
}
