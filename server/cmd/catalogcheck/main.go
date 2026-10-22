// Command catalogcheck validates a systems catalog directory:
//
//	go run ./server/cmd/catalogcheck [dir]   (default: ./systems)
package main

import (
	"fmt"
	"os"

	"pdp11emu/server/internal/catalog"
)

func main() {
	dir := "systems"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	cat, err := catalog.Load(os.DirFS(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	errs := cat.Validate()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e)
	}
	for _, s := range cat.Systems {
		fmt.Printf("%-12s %-16s %-15s %s\n", s.ID, s.License.Class, s.Speed, s.Name)
	}
	if len(errs) > 0 {
		os.Exit(1)
	}
}
