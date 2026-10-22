// Package deps pins the third-party modules used by pdp11d so that
// go.mod/go.sum stay stable while packages are developed in parallel.
// Remove an import here only when no package uses that module any more.
package deps

import (
	_ "github.com/coder/websocket"
	_ "github.com/ulikunitz/xz"
)
