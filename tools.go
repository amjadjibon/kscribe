//go:build tools

package tools

import (
	_ "github.com/a-h/templ/cmd/templ"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
