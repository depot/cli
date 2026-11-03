package projects

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func writeTrustPolicyJSON(w io.Writer, msg proto.Message) error {
	data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true, Indent: "  "}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func writeTrustPolicy(w io.Writer, policy *corev1.TrustPolicy, output string) error {
	if policy == nil {
		return fmt.Errorf("API returned no trust policy")
	}
	if output == "json" {
		return writeTrustPolicyJSON(w, policy)
	}
	return writeTrustPolicies(w, []*corev1.TrustPolicy{policy})
}

func writeTrustPolicies(w io.Writer, policies []*corev1.TrustPolicy) error {
	if len(policies) == 0 {
		_, err := fmt.Fprintln(w, "No trust policies found.")
		return err
	}
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "POLICY ID\tPROVIDER\tORGANIZATION / OWNER\tREPOSITORY / PROJECT / PIPELINE\tRESTRICTIONS")
	for _, policy := range policies {
		provider, org, resource, restrictions := "unknown", "-", "-", "unknown"
		switch p := policy.GetProvider().(type) {
		case *corev1.TrustPolicy_Github:
			provider, org, resource = "github", p.Github.GetRepositoryOwner(), p.Github.GetRepository()
			restrictions = fmt.Sprintf("allow-public-pull-requests=%t", p.Github.GetAllowPublicPullRequests())
		case *corev1.TrustPolicy_Gitlab:
			provider, org, resource = "gitlab", p.Gitlab.GetNamespaceId(), p.Gitlab.GetProjectId()
			restrictions = fmt.Sprintf("protected-refs-only=%t", p.Gitlab.GetProtectedRefsOnly())
		case *corev1.TrustPolicy_Circleci:
			provider, org, resource, restrictions = "circleci", p.Circleci.GetOrganizationUuid(), p.Circleci.GetProjectUuid(), "-"
		case *corev1.TrustPolicy_Buildkite_:
			provider, org, resource, restrictions = "buildkite", p.Buildkite.GetOrganizationSlug(), p.Buildkite.GetPipelineSlug(), "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", trustPolicyCell(policy.GetTrustPolicyId()), provider, trustPolicyCell(org), trustPolicyCell(resource), restrictions)
	}
	return table.Flush()
}

func trustPolicyCell(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
}
