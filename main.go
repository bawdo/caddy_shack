package main

import (
	"os"

	"github.com/bawdo/caddy_shack/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
