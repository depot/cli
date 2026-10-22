target "selector" {
  project_id = "vtproject-policy"
}

target "collision" {
  policy = [{ filename = "depot-project:deny.rego" }]
  output = ["type=local,dest=out"]
}
