package httpclient

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lestrrat/go-hsup/ext"
	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-hsup/internal/parser"
	"github.com/lestrrat/go-jshschema"
)

type Builder struct {
	AppPkg    string
	ClientPkg string
	Overwrite bool
	PkgPath   string
}

type genctx struct {
	*parser.Result
	AppPkg    string
	ClientPkg string
	Overwrite bool
	PkgPath   string
}

func New() *Builder {
	return &Builder{
		AppPkg:    "app",
		ClientPkg: "client",
		Overwrite: false,
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
	ctx := genctx{
		AppPkg:    b.AppPkg,
		ClientPkg: b.ClientPkg,
		Overwrite: b.Overwrite,
		PkgPath:   b.PkgPath,
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

	for _, link := range s.Links {
		methodName := genutil.TitleToName(link.Title)
		methodBody, err := makeMethod(ctx, methodName, link)
		if err != nil {
			return err
		}
		ctx.Methods[methodName] = methodBody
	}

	sort.Strings(ctx.MethodNames)
	return nil
}

func makeMethod(ctx *genctx, name string, l *hschema.Link) (string, error) {
	intype := "interface{}"
	outtype := ""
	if s := l.Schema; s != nil {
		if !s.IsResolved() {
			rs, err := s.Resolve(ctx.Schema)
			if err != nil {
				return "", err
			}
			s = rs
		}
		if t, ok := s.Extras[ext.TypeKey]; ok {
			if ts, ok := t.(string); ok {
				intype = ts
			}
		}
	}
	if s := l.TargetSchema; s != nil {
		if !s.IsResolved() {
			rs, err := s.Resolve(ctx.Schema)
			if err != nil {
				return "", err
			}
			s = rs
		}
		outtype = "interface{}"
		if t, ok := s.Extras[ext.TypeKey]; ok {
			if ts, ok := t.(string); ok {
				outtype = ts
			}
		}
	}
	buf := bytes.Buffer{}
	fmt.Fprintf(&buf, `func (c *Client) %s(in `, name)

	if genutil.LooksLikeStruct(intype) {
		buf.WriteRune('*')
	}
	fmt.Fprintf(&buf, `%s) `, intype)

	if outtype == "" {
		buf.WriteString(`error {`)
	} else {
		prefix := ""
		if genutil.LooksLikeStruct(outtype) {
			prefix = "*"
		}

		fmt.Fprintf(&buf, `(%s%s, error) {`, prefix, outtype)
	}

	errbuf := bytes.Buffer{}
	errbuf.WriteString("\nif err != nil {")
	if outtype == "" {
		errbuf.WriteString("\nreturn err")
	} else {
		errbuf.WriteString("\nreturn nil, err")
	}
	errbuf.WriteString("\n}")
	errout := errbuf.String()

	fmt.Fprintf(&buf, "\n"+`u, err := url.Parse(c.BaseURL + %s)`, strconv.Quote(l.Path()))
	buf.WriteString(errout)

	buf.WriteString("\n" + `buf := bytes.Buffer{}`)
	buf.WriteString("\n" + `err = json.NewEncoder(&buf).Encode(in)`)
	buf.WriteString(errout)

	switch strings.ToLower(l.Method) {
	case "get":
		buf.WriteString("\n" + `res, err := c.Client.Get(u.String())`)
		buf.WriteString(errout)
	case "post":
		buf.WriteString("\n" + `res, err := c.Client.Post(u.String(), "text/json", &buf)`)
		buf.WriteString(errout)
	}
	buf.WriteString("\nif res.StatusCode != http.StatusOK {")
	buf.WriteString("\nreturn ")
	if outtype != "nil" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("fmt.Errorf(`Invalid response: '%%s'`, res.Status)")
	buf.WriteString("\n}")
	if outtype != "" {
		buf.WriteString("\nvar payload ")
		if genutil.LooksLikeStruct(outtype) {
			buf.WriteRune('*')
		}
		buf.WriteString(outtype)
		buf.WriteString("\nerr := json.NewDecoder(res.Body).Decode(payload)")
		buf.WriteString(errout)
		buf.WriteString("\nreturn payload, nil")
	}
	buf.WriteString("\n}")

	return buf.String(), nil
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

func generateFiles(ctx *genctx) error {
	{
		fn := filepath.Join(ctx.AppPkg, "client", "client.go")
		if err := generateFile(ctx, fn, generateClientCode); err != nil {
			return err
		}
	}

	return nil
}

func generateClientCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	genutil.WriteDoNotEdit(&buf)
	fmt.Fprintf(&buf, "package %s\n\n", ctx.ClientPkg)

	genutil.WriteImports(
		&buf,
		[]string{"bytes", "encoding/json", "fmt", "net/http", "net/url"},
		nil,
	)

	// for each endpoint, create a method that accepts
	for _, method := range ctx.Methods {
		fmt.Fprint(&buf, method)
		fmt.Fprint(&buf, "\n\n")
	}

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		return err
	}
	if _, err := out.Write(fsrc); err != nil {
		return err
	}

	return nil
}
