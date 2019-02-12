package hsup

// Package hsup processes JSON Hyper Schema files to generated
// skeleton web applications.
//
// /* generate net/http compliant code */
// hsup.NetHTTP.ProcessFile(schemaFile)

import (
	"github.com/lestrrat-go/jshschema"
)

type Processor interface {
	Process(*hschema.HyperSchema) error
	ProcessFile(string) error
}

type Options struct {
	Dir       string `short:"d" long:"dir" required:"true" description:"Directory to place all files under"`
	PkgPath   string
	AppPkg    string   `short:"a" long:"apppkg" description:"Application package name"`
	Schema    string   `short:"s" long:"schema" required:"true" description:"schema file to process"`
	Flavor    []string `short:"f" long:"flavor" default:"nethttp" default:"validator" default:"httpclient" description:"what type of code to generate"`
	Overwrite bool     `short:"O" long:"overwrite" description:"overwrite if file exists"`
	GoVersion string   `short:"g" long:"goversion" description:"Go version to assume" default:"1.7"`
	Args      []string // left over arguments
}
