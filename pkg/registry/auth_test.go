package registry

import "testing"

func TestGetDepotAuthConfigForHostUsesDepotTokenForDepotRegistry(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "depot_cijob_test")

	for _, host := range []string{"registry.depot.dev", "org123.registry.depot.dev"} {
		t.Run(host, func(t *testing.T) {
			creds := GetDepotAuthConfigForHost(host)
			if creds == nil {
				t.Fatal("expected credentials")
			}
			if creds.Username != "x-token" {
				t.Fatalf("Username = %q, want x-token", creds.Username)
			}
			if creds.Password != "depot_cijob_test" {
				t.Fatalf("Password = %q, want DEPOT_TOKEN", creds.Password)
			}
		})
	}
}

func TestGetDepotAuthConfigForHostDoesNotUseDepotTokenForOtherRegistries(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "depot_cijob_test")

	for _, host := range []string{"docker.io", "registry.depot.dev.evil.com", "notregistry.depot.dev"} {
		t.Run(host, func(t *testing.T) {
			if creds := GetDepotAuthConfigForHost(host); creds != nil {
				t.Fatalf("expected no credentials, got %#v", creds)
			}
		})
	}
}

func TestGetDepotAuthConfigForHostPrefersExplicitPushRegistryAuth(t *testing.T) {
	t.Setenv("DEPOT_TOKEN", "depot_cijob_test")
	t.Setenv("DEPOT_PUSH_REGISTRY_USERNAME", "push-user")
	t.Setenv("DEPOT_PUSH_REGISTRY_PASSWORD", "push-password")

	creds := GetDepotAuthConfigForHost("org123.registry.depot.dev")
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.Username != "push-user" {
		t.Fatalf("Username = %q, want explicit push registry username", creds.Username)
	}
	if creds.Password != "push-password" {
		t.Fatalf("Password = %q, want explicit push registry password", creds.Password)
	}
}
