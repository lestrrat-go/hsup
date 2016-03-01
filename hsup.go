package hsup

// Package hsup processes JSON Hyper Schema files to generated
// skeleton web applications.

import (
	"github.com/lestrrat/go-hsup/flavor"
	"github.com/lestrrat/go-jshschema"
)

type Builder struct {
	flavor flavor.Flavor
}

func New() *Builder {
	return &Builder{
		flavor: flavor.NetHTTP,
	}
}

func (b *Builder) Process(s *hschema.HyperSchema) error {
	f := b.flavor
	ctx := f.MakeContext()
	if err := f.Parse(ctx, s); err != nil {
		return err
	}
	if err := f.GenerateFiles(ctx); err != nil {
		return err
	}
	return nil
}

func (b *Builder) ProcessFile(f string) error {
	s, err := hschema.ReadFile(f)
	if err != nil {
		return err
	}
	return b.Process(s)
}
