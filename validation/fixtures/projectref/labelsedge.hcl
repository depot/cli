variable "PROJECT" {
  default = "vtproject-heredoc"
}

target "_base" {
  project_id = "vtproject-edge"
  labels     = { foo = "bar" }
}

target "_unlabeled" {
  project_id = "vtproject-unlabeled"
}

target "emptyproject" {
  inherits   = ["_base"]
  project_id = ""
  target     = "artifact"
}

target "fallbacklabels" {
  target = "artifact"
  labels = coalesce(target._unlabeled.labels, { fallback = "yes" })
}

target "maptype" {
  target = "artifact"
  args = {
    EQUAL = "${target._base.labels == { foo = "bar" }}"
    JSON  = jsonencode(target._base.labels)
  }
}

target "heredocproject" {
  project_id = <<-EOF
    ${PROJECT~}
  EOF
  target = "artifact"
}

target "selfproject" {
  project_id = "vtproject-self"
  target     = "artifact"
  labels     = { project = target.selfproject.project_id }
}

group "edge" {
  targets = ["emptyproject", "fallbacklabels", "maptype", "heredocproject", "selfproject"]
}
