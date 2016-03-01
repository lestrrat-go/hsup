package hsup

// Package hsup processes JSON Hyper Schema files to generated
// skeleton web applications.
//
// /* generate net/http compliant code */
// hsup.NetHTTP.ProcessFile(schemaFile)

import (
	"github.com/lestrrat/go-hsup/nethttp"
	"github.com/lestrrat/go-jshschema"
)

type Processor interface {
	Process(*hschema.HyperSchema) error
	ProcessFile(string) error
}

// NetHTTP implements the scaffold generator that generates
// net/http compliant code.
var NetHTTP = nethttp.New()
