variable "TAG" {
  default = "latest"
}

variable "REGISTRY" {
  default = "registry.invalid"
}

function "image" {
  params = [name]
  result = "${REGISTRY}/${name}:${TAG}"
}

group "default" {
  targets = ["app", "artifact"]
}

group "all" {
  targets = ["app", "artifact", "variant"]
}

target "base" {
  dockerfile = "Dockerfile"
  args = {
    MESSAGE = "from-base"
  }
  labels = {
    "org.depot.validation.base" = "true"
  }
}

target "app" {
  inherits = ["base"]
  target   = "image"
  tags     = [image("app")]
}

target "artifact" {
  inherits = ["base"]
  target   = "artifact"
  args = {
    MESSAGE = "from-artifact"
  }
  output = ["type=local,dest=out/artifact"]
}

target "variant" {
  name     = "variant-${item}"
  matrix   = { item = ["one", "two"] }
  inherits = ["base"]
  target   = "artifact"
  args = {
    MESSAGE = item
  }
  output = ["type=local,dest=out/variant-${item}"]
}
