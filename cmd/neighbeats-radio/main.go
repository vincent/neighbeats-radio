// Command neighbeats-radio runs and controls the station director.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

const usage = `usage: neighbeats-radio [-config FILE] <command>

commands:
  run          run the director (long-running)
  queue        print the planned queue
  push PATH    push a file into Liquidsoap's music queue
  skip         skip the current track
  version      print the version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "neighbeats-radio:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("missing command")
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
