package docker

default allow := false

decision := {"allow": allow, "deny_msg": ["denied by deny.rego"]}
