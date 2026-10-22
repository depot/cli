target "default" {
  output = ["type=local,dest=out/default"]
}

# A SOURCE_DATE_EPOCH argument of the target takes precedence over the
# environment.
target "pinned" {
  args   = { SOURCE_DATE_EPOCH = "1600000000" }
  output = ["type=local,dest=out/pinned"]
}
