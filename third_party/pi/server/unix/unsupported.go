//go:build !unix

// Package unix is the Go port of pi's Unix-domain-socket transport for the
// session server. It carries no API on this platform: there are no
// Unix-domain sockets to bind, so there is nothing here to configure. Serve on
// a different server.Listener instead.
//
// This file exists so the package still builds — and builds empty — everywhere
// else, rather than failing the build of anything that walks ./... on Windows.
package unix
