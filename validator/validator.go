package validator

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-hsup/internal/parser"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsval"
)

type Builder struct {
	AppPkg       string
	Overwrite    bool
	PkgPath      string
	ValidatorPkg string
}

type genctx struct {
	*parser.Result
	AppPkg       string
	Overwrite    bool
	PkgPath      string
	ValidatorPkg string
}

func New() *Builder {
	return &Builder{
		Overwrite:    false,
		ValidatorPkg: "validator",
	}
}

func (b *Builder) ProcessFile(f string) error {
	log.Printf(" ===> Using schema file '%s'", f)
	s, err := hschema.ReadFile(f)
	if err != nil {
		return err
	}
	return b.Process(s)
}

func (b *Builder) Process(s *hschema.HyperSchema) error {
	if b.AppPkg == "" {
		return errors.New("AppPkg cannot be empty")
	}

	if b.PkgPath == "" {
		return errors.New("PkgPath cannot be empty")
	}

	ctx := genctx{
		AppPkg:       b.AppPkg,
		Overwrite:    b.Overwrite,
		PkgPath:      b.PkgPath,
		ValidatorPkg: b.ValidatorPkg,
	}

	if err := parse(&ctx, s); err != nil {
		return err
	}

	if err := generateFiles(&ctx); err != nil {
		return err
	}

	log.Printf(" <=== All files generated")
	return nil
}

func parse(ctx *genctx, s *hschema.HyperSchema) error {
	pres, err := parser.Parse(s)
	if err != nil {
		return err
	}
	ctx.Result = pres
	return nil
}

func generateFiles(ctx *genctx) error {
	{
		fn := filepath.Join(ctx.AppPkg, ctx.ValidatorPkg, fmt.Sprintf("%s.go", ctx.ValidatorPkg))
		if err := generateFile(ctx, fn, generateValidatorCode); err != nil {
			return err
		}
	}

	return nil
}

func generateFile(ctx *genctx, fn string, cb func(io.Writer, *genctx) error) error {
	if _, err := os.Stat(fn); err == nil {
		if !ctx.Overwrite {
			log.Printf(" - File '%s' already exists. Skipping", fn)
			return nil
		}
		log.Printf(" * File '%s' already exists. Overwriting", fn)
	}

	log.Printf(" + Generating file '%s'", fn)
	f, err := genutil.CreateFile(fn)
	if err != nil {
		return err
	}
	defer f.Close()
	return cb(f, ctx)
}

func generateValidatorCode(out io.Writer, ctx *genctx) error {
	g := jsval.NewGenerator()
	validators := make([]*jsval.JSVal, 0, len(ctx.RequestValidators)+len(ctx.ResponseValidators))
	for _, v := range ctx.RequestValidators {
		validators = append(validators, v)
	}
	for _, v := range ctx.ResponseValidators {
		validators = append(validators, v)
	}

	buf := bytes.Buffer{}
	genutil.WriteDoNotEdit(&buf)
	buf.WriteString("package " + ctx.ValidatorPkg + "\n\n")

	genutil.WriteImports(
		&buf,
		nil,
		[]string{
			"github.com/lestrrat/go-jsval",
		},
	)
	if err := g.Process(&buf, validators...); err != nil {
		return err
	}
	buf.WriteString("\n\n")

	return genutil.WriteFmtCode(out, &buf)
}
