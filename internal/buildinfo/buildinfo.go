// Package buildinfo contains identity injected into published and repository
// builds. The root VERSION file remains the only numeric release authority.
package buildinfo

// Version is "dev" for an uninjected build, <VERSION>+dev for a repository
// build, and exactly <VERSION> for a published release.
var Version = "dev"
