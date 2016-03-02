package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jessevdk/go-flags"
	"github.com/lestrrat/go-hsup"
)

func main() {
	os.Exit(_main())
}

type options struct {
	Dir       string `short:"d" long:"dir" required:"true" description:"Directory to place all files under"`
	PkgPath   string
	AppPkg    string   `short:"a" long:"apppkg" description:"Application package name"`
	Schema    string   `short:"s" long:"schema" required:"true" description:"schema file to process"`
	Flavor    []string `short:"f" long:"flavor" default:"nethttp" description:"what type of code to generate"`
	Overwrite bool     `short:"O" long:"overwrite" default:"false" description:"overwrite if file exists"`
}

func _main() int {
	var opts options
	if _, err := flags.Parse(&opts); err != nil {
		log.Printf("%s", err)
		return 1
	}

	// opts.Dir better be under GOPATH
	for _, path := range strings.Split(os.Getenv("GOPATH"), string([]rune{filepath.ListSeparator})) {
		path, err := filepath.Abs(path)
		if err != nil {
			log.Printf("%s", err)
			return 1
		}
		path = filepath.Join(path, "src")
		dir, err := filepath.Abs(opts.Dir)
		if err != nil {
			log.Printf("%s", err)
			return 1
		}

		if strings.HasPrefix(dir, path) {
			opts.PkgPath = strings.TrimPrefix(strings.TrimPrefix(dir, path), string([]rune{filepath.Separator}))
			break
		}
	}

	if opts.PkgPath == "" {
		log.Printf("Target path should be under GOPATH")
		return 1
	}

	// Unless otherwise specified, last portion of the PkgPath is
	// the AppPkg
	if opts.AppPkg == "" {
		opts.AppPkg = filepath.Base(opts.PkgPath)
	}

	var cb func(options) error
	for _, f := range opts.Flavor {
		log.Printf(" ===> running flavor '%s'", f)
		switch f {
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
	}
	return 0
}

func doNetHTTP(opts options) error {
	b := hsup.NetHTTP
	b.AppPkg = opts.AppPkg
	b.PkgPath = opts.PkgPath
	b.Overwrite = opts.Overwrite
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}
	return nil
}

func doHTTPClient(opts options) error {
	b := hsup.HTTPClient
	b.AppPkg = opts.AppPkg
	b.PkgPath = opts.PkgPath
	b.Overwrite = opts.Overwrite
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}
	return nil
}
