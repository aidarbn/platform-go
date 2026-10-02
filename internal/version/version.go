// Package version tells which platform version this binary is: the version written
// into platformgo.yaml, go.mod and platformgo.lock of the projects it creates and applies.
package version

import (
	"runtime/debug"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Module is the path of the platform module.
const Module = "github.com/aidarbn/platform-go"

// Fallback is the version of a build from a checkout, which carries no module version.
// `make release` bumps it and CI fails a tag that does not match it; a released binary
// — go tool, go run or go install at a version — never reads it.
const Fallback = "v0.5.9"

// Platform returns the platform version of the running binary.
func Platform() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Fallback
	}
	return fromBuildInfo(info)
}

// fromBuildInfo finds the platform among the main module and the dependencies. A
// command built from the platform module — go install, go run at a version, and
// `go tool platformgo` inside a project too — has the platform as its main module.
func fromBuildInfo(info *debug.BuildInfo) string {
	if info.Main.Path == Module {
		// A replaced platform is a local checkout: its version says nothing about the code.
		if info.Main.Replace != nil {
			return Fallback
		}
		if v := release(info.Main.Version); v != "" {
			return v
		}
		return Fallback
	}
	for _, dep := range info.Deps {
		if dep.Path != Module {
			continue
		}
		// A replaced platform is a local checkout: its version says nothing about the code.
		if dep.Replace == nil {
			if v := release(dep.Version); v != "" {
				return v
			}
		}
		break
	}
	return Fallback
}

// release keeps tagged versions only: a pseudo-version names no release, so no schema
// URL or upgrade can point at it.
func release(v string) string {
	if !semver.IsValid(v) || module.IsPseudoVersion(v) {
		return ""
	}
	return v
}
