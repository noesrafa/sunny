// Package defaults exposes the seed runtime tree baked into the binary.
// On first run, sunny copies these files into ~/.sunny/.
package defaults

import "embed"

//go:embed all:agents
var FS embed.FS
