// Command retroarch manages the ROM library and Apple TV RetroArch setup.
package main

import (
	"fmt"
	"os"

	"github.com/fabiogoma/retroarch/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
