// Package configs carries the committed client configurations into the
// binary, so the gateway writes each client's files from the one copy of each
// (internal/service/clientconfig) rather than from a second one kept beside
// the code that rewrites it. The files stay where they are, readable in the
// repository and linked from the docs; go:embed reaches only below the file
// that names it, which is why this package sits beside them.
package configs

import "embed"

//go:embed clients
var clients embed.FS

// Client is the committed file of that name under configs/clients, or "" for
// one there is not.
func Client(name string) string {
	b, err := clients.ReadFile("clients/" + name)
	if err != nil {
		return ""
	}
	return string(b)
}
