package main

// version is the build version. It is overridden at build time via
//   -ldflags "-X main.version=<tag>"
// (see .github/workflows/release.yml). For local `go build` it stays "dev".
var version = "dev"
