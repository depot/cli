group "default" {
  targets = ["app", "db"]
}

target "app" {
  dockerfile = "Dockerfile"
  platforms = ["linux/amd64", "linux/arm64"]
}

target "db" {
  dockerfile = "Dockerfile"
  platforms = ["linux/amd64", "linux/arm64"]
}
