target "_base" {
  project_id  = "vtproject-base"
  description = "base description"
}
target "described" {
  target = "artifact"
  args = { BASE_DESCRIPTION = target._base.description }
}
