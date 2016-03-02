package main

import (
	"log"
	"os"

	"github.com/lestrrat/go-hsup"
	"github.com/jessevdk/go-flags"
)

func main() {
	os.Exit(_main())
}

type options struct {
	Schema string `short:"s" long:"schema" required:"true" description:"schema file to process"`
	Flavor string `short:"f" long:"flavor" default:"nethttp" description:"what type of code to generate"`
}
func _main() int {
	var opts options
	if _, err := flags.Parse(&opts); err != nil {
		log.Printf("%s", err)
		return 1
	}

	var cb func(options) error
	switch opts.Flavor {
	case "nethttp":
		cb = doNetHTTP
	case "httpclient":
		cb = doHTTPClient
	default:
		log.Printf("unknown argument to `flavor`: %s", opts.Flavor)
		return 1
	}

	if err := cb(opts); err != nil {
		log.Printf("%s", err)
		return 1
	}
	return 0
}

func doNetHTTP(opts options) error {
	b := hsup.NetHTTP
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}
	return nil
}

func doHTTPClient(opts options) error {
	b := hsup.HTTPClient
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}
	return nil
}
