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

	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-pdebug"
)

type Builder struct {
	AppPkg       string
	ClientPkg    string
	Overwrite    bool
	PkgPath      string
	ValidatorPkg string
}

type genctx struct {
	apppkg             string
	schema             *hschema.HyperSchema
	clientpkg          string
	validatorpkg       string
	methods            map[string]string
	methodPayloadType  map[string]string
	methodNames        []string
	overwrite          bool
	pathToMethods      map[string]string
	pkgpath            string
	requestValidators  map[string]*jsval.JSVal
	responseValidators map[string]*jsval.JSVal
}

func New() *Builder {
	return &Builder{
		ClientPkg:    "client",
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
	ctx := genctx{
		schema:             s,
		methodNames:        make([]string, len(s.Links)),
		apppkg:             b.AppPkg,
		clientpkg:          b.ClientPkg,
		methods:            make(map[string]string),
		methodPayloadType:  make(map[string]string),
		pkgpath:            b.PkgPath,
		pathToMethods:      make(map[string]string),
		requestValidators:  make(map[string]*jsval.JSVal),
		responseValidators: make(map[string]*jsval.JSVal),
		validatorpkg:       b.ValidatorPkg,
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
	for i, link := range s.Links {
		methodName := genutil.TitleToName(link.Title)
		ctx.methodPayloadType[methodName] = "interface{}"

		// Got to do this first, because validators are used in makeMethod()
		if s := link.Schema; s != nil {
			v, err := genutil.MakeValidator(s, ctx.schema)
			if err != nil {
				return err
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.requestValidators[methodName] = v
		}
		if s := link.TargetSchema; s != nil {
			v, err := genutil.MakeValidator(s, ctx.schema)
			if err != nil {
				return err
			}
			v.Name = fmt.Sprintf("HTTP%sResponse", methodName)
			ctx.responseValidators[methodName] = v
		}
		if ls := link.Schema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.schema)
				if err != nil {
					return err
				}
				ls = rs
			}
			if pdebug.Enabled {
				pdebug.Printf("checking extras for %s: %#v", link.Path(), ls.Extras)
			}
			if gt, ok := ls.Extras["gotype"]; ok {
				ctx.methodPayloadType[methodName] = gt.(string)
			}
		}
		ctx.methodNames[i] = methodName
		ctx.pathToMethods[link.Path()] = methodName
		methodBody, err := makeMethod(ctx, methodName, link)
		if err != nil {
			return err
		}
		ctx.methods[methodName] = methodBody
	}

	sort.Strings(ctx.methodNames)
	return nil
}

func makeMethod(ctx *genctx, name string, l *hschema.Link) (string, error) {
	intype := "interface{}"
	outtype := ""
	if s := l.Schema; s != nil {
		if !s.IsResolved() {
			rs, err := s.Resolve(ctx.schema)
			if err != nil {
				return "", err
			}
			s = rs
		}
		if t, ok := s.Extras["gotype"]; ok {
			if ts, ok := t.(string); ok {
				intype = ts
			}
		}
	}
	if s := l.TargetSchema; s != nil {
		if !s.IsResolved() {
			rs, err := s.Resolve(ctx.schema)
			if err != nil {
				return "", err
			}
			s = rs
		}
		outtype = "interface{}"
		if t, ok := s.Extras["gotype"]; ok {
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
		if !ctx.overwrite {
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
		fn := filepath.Join(ctx.apppkg, "client", "client.go")
		if err := generateFile(ctx, fn, generateClientCode); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.apppkg, ctx.validatorpkg, fmt.Sprintf("%s.go", ctx.validatorpkg))
		if err := generateFile(ctx, fn, generateValidatorCode); err != nil {
			return err
		}
	}

	return nil
}

func generateClientCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, `package %s`, ctx.clientpkg)
	buf.WriteString("\n\n")

	genutil.WriteImports(
		&buf,
		[]string{"bytes", "encoding/json", "fmt", "net/http", "net/url"},
		nil,
	)

	// for each endpoint, create a method that accepts
	for _, method := range ctx.methods {
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
func generateValidatorCode(out io.Writer, ctx *genctx) error {
	g := jsval.NewGenerator()
	validators := make([]*jsval.JSVal, 0, len(ctx.requestValidators)+len(ctx.responseValidators))
	for _, v := range ctx.requestValidators {
		validators = append(validators, v)
	}
	for _, v := range ctx.responseValidators {
		validators = append(validators, v)
	}

	buf := bytes.Buffer{}
	buf.WriteString("package " + ctx.validatorpkg + "\n\n")

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

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		return err
	}

	if _, err := out.Write(fsrc); err != nil {
		return err
	}

	return nil
}
