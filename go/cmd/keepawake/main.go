package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	resource "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/wake"
)

const name = "keepawake"

func main() {
	endpoint := flag.String("endpoint", resource.DefaultEndpoint(), "where the resource service listens")
	flag.String("state", "", "legacy registration state (unused)")
	flag.Parse()
	why := strings.Join(flag.Args(), " ")
	if why == "" {
		why = "asked to at the command line"
	}
	lease, err := (wake.Holder{Client: resource.New(*endpoint)}).Hold(name, why)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("holding the machine awake: %s\nCtrl-C releases it\n", why)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	select {
	case <-stop:
		if err := lease.Release(); err != nil {
			fail("%v", err)
		}
		fmt.Println("released")
	case <-lease.Done():
		fmt.Println("the wake lease ended: its rule was revoked, or the service stopped")
		os.Exit(3)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
