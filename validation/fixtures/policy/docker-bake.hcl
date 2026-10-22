target "denied" {
  policy = [{ filename = "deny.rego" }]
  output = ["type=local,dest=out"]
}
