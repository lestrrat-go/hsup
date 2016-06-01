package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jessevdk/go-flags"
	"github.com/lestrrat/go-hsup"
	"github.com/lestrrat/go-hsup/httpclient"
	"github.com/lestrrat/go-hsup/nethttp"
	"github.com/lestrrat/go-hsup/validator"
	"github.com/pkg/errors"
)

func main() {
	if err := _main(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func _main() error {
	// Remove every option that is prefixed
	prefixes := map[string][]string{
		"nethttp": nil,
		"httpclient": nil,
		"validator": nil,
	}

	var mainargs []string
OUTER:
	for i := 1; i < len(os.Args); i++ {
		v := os.Args[i]
		for prefix := range prefixes {
			// --prefix.localname=var or --prefix.localname var
			if !strings.HasPrefix(v, "--" + prefix + ".") {
				continue
			}

			localname := v[len(prefix)+3:]
			if len(localname) == 0 || localname[0] == '=' {
				return errors.New("prefixed parameter must have a local name: " + prefix)
			}

			l := prefixes[prefix]
			l = append(l, "--" + localname)
			if len(os.Args) > i + 1 {
				if nextv := os.Args[i+1]; len(nextv) > 0 && nextv[0] != '-' {
					l = append(l, os.Args[i+1])
					i++
				}
			}
			prefixes[prefix] = l
			continue OUTER
		}

		mainargs = append(mainargs, v)
	}

	var opts hsup.Options
	if _, err := flags.ParseArgs(&opts, mainargs); err != nil {
		return errors.Wrap(err, "failed to parse arguments")
	}

	// opts.Dir better be under GOPATH
	for _, path := range strings.Split(os.Getenv("GOPATH"), string([]rune{filepath.ListSeparator})) {
		path, err := filepath.Abs(path)
		if err != nil {
			return errors.Wrap(err, "failed to get absolute path")
		}
		path = filepath.Join(path, "src")
		dir, err := filepath.Abs(opts.Dir)
		if err != nil {
			return errors.Wrap(err, "failed to get absolute dir")
		}

		if strings.HasPrefix(dir, path) {
			opts.PkgPath = strings.TrimPrefix(strings.TrimPrefix(dir, path), string([]rune{filepath.Separator}))
			break
		}
	}

	if opts.PkgPath == "" {
		return errors.New("target path should be under GOPATH")
	}

	// Unless otherwise specified, last portion of the PkgPath is
	// the AppPkg
	if opts.AppPkg == "" {
		opts.AppPkg = filepath.Base(opts.PkgPath)
	}

	var cb func(hsup.Options) error
	for _, f := range opts.Flavor {
		log.Printf(" ===> running flavor '%s'", f)
		switch f {
		case "nethttp":
			cb = nethttp.Process
		case "httpclient":
			cb = httpclient.Process
		case "validator":
			cb = validator.Process
		default:
			return errors.New("unknown argument to `flavor`: " + f)
		}
		opts.Args = prefixes[f]

		if err := cb(opts); err != nil {
			return errors.Wrap(err, "failed to execute handler")
		}
	}
	return nil
}
