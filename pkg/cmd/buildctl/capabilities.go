package buildctl

import (
	"slices"

	apicaps "github.com/moby/buildkit/util/apicaps/pb"
)

// The proxy answers capability requests without starting a builder, so it
// reports a fixed list: the capabilities of the buildkit v0.13.2 fork that
// runs on Depot builders.
var (
	gatewayCapabilities = capabilityList([]string{
		"frontend.caps",
		"frontend.inputs",
		"gateway.evaluate",
		"gateway.exec",
		"gateway.exec.extrahosts",
		"gateway.exec.secretenv",
		"gateway.exec.signals",
		"gateway.solve.evaluate",
		"gateway.solve.metadata",
		"gateway.warnings",
		"importcaches",
		"proto.refarray",
		"readdir",
		"readfile",
		"reference.attestations",
		"reference.output",
		"resolveimage",
		"resolveimage.resolvemode",
		"return",
		"returnmap",
		"solve.base",
		"solve.inlinereturn",
		"source.metaresolver",
		"statfile",
	})
	llbCapabilities = capabilityList([]string{
		"cache.azblob",
		"cache.gha",
		"cache.s3",
		"constraints",
		"diffop",
		"exec.cgroup",
		"exec.meta.base",
		"exec.meta.cgroup.parent",
		"exec.meta.network",
		"exec.meta.proxyenv",
		"exec.meta.security",
		"exec.meta.security.devices.v1",
		"exec.meta.setsdefaultpath",
		"exec.meta.ulimit",
		"exec.mount.bind",
		"exec.mount.bind.readwrite-nooutput",
		"exec.mount.cache",
		"exec.mount.cache.content",
		"exec.mount.cache.sharing",
		"exec.mount.secret",
		"exec.mount.selector",
		"exec.mount.ssh",
		"exec.mount.tmpfs",
		"exec.mount.tmpfs.size",
		"exec.secretenv",
		"exporter.image.annotations",
		"exporter.image.attestations",
		"exporter.multiple",
		"exporter.sourcedateepoch",
		"file.base",
		"file.copy.includeexcludepatterns",
		"file.rm.nofollowsymlink",
		"file.rm.wildcard",
		"mergeop",
		"meta.description",
		"meta.exportcache",
		"meta.ignorecache",
		"platform",
		"soruce.http.uidgid",
		"source.buildop.llbfilename",
		"source.git",
		"source.git.fullurl",
		"source.git.httpauth",
		"source.git.keepgitdir",
		"source.git.knownsshhosts",
		"source.git.mountsshsock",
		"source.git.subdir",
		"source.http",
		"source.http.checksum",
		"source.http.perm",
		"source.image",
		"source.image.layerlimit",
		"source.image.resolvemode",
		"source.local",
		"source.local.differ",
		"source.local.excludepatterns",
		"source.local.followpaths",
		"source.local.includepatterns",
		"source.local.sessionid",
		"source.local.sharedkeyhint",
		"source.local.unique",
		"source.ocilayout",
		"source.policy",
	})
	deprecatedCapabilities = []string{"solve.inlinereturn"}
)

func capabilityList(ids []string) []*apicaps.APICap {
	caps := make([]*apicaps.APICap, len(ids))
	for i, id := range ids {
		caps[i] = &apicaps.APICap{ID: id, Enabled: true, Deprecated: slices.Contains(deprecatedCapabilities, id)}
	}
	return caps
}
