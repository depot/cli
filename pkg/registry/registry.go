package registry

import "fmt"

const Registry = "registry.depot.dev"

func DepotImageName(orgID, projectID, buildID string) string {
	return fmt.Sprintf("%s/%s/%s:%s", Registry, orgID, projectID, buildID)
}
