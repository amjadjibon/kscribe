package public

import "embed"

//go:embed all:css all:js all:icons all:fonts
var FS embed.FS
