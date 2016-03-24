package httpclient

import (
	"bytes"
	"errors"
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
	"github.com/lestrrat/go-hsup/internal/parser"
	"github.com/lestrrat/go-jshschema"
)

type Builder struct {
	AppPkg    string
	ClientPkg string
	Dir       string
	Overwrite bool
	PkgPath   string
}

type clientHints struct {
	Imports []string
}

type genctx struct {
	*parser.Result
	AppPkg      string
	ClientHints clientHints
	ClientPkg   string
	Dir         string
	Overwrite   bool
	PkgPath     string
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
		Dir:       b.Dir,
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

func parseClientHints(ctx *genctx, m map[string]interface{}) error {
	if v, ok := m["imports"]; ok {
		switch v.(type) {
		case []interface{}:
		default:
			return errors.New("invalid value type for imports: expected []interface{}")
		}

		l := v.([]interface{})
		ctx.ClientHints.Imports = make([]string, len(l))
		for i, n := range l {
			switch n.(type) {
			case string:
			default:
				return errors.New("invalid value type for elements in imports: expected string")
			}
			ctx.ClientHints.Imports[i] = n.(string)
		}
	}
	return nil
}

func parseExtras(ctx *genctx, s *hschema.HyperSchema) error {
	for k, v := range s.Extras {
		switch k {
		case "hsup.client":
			switch v.(type) {
			case map[string]interface{}:
			default:
				return errors.New("invalid value type for hsup.client: expected map[string]interface{}")
			}

			if err := parseClientHints(ctx, v.(map[string]interface{})); err != nil {
				return err
			}
		}
	}
	return nil
}

func parse(ctx *genctx, s *hschema.HyperSchema) error {
	pres, err := parser.Parse(s)
	if err != nil {
		return err
	}
	ctx.Result = pres

	if err := parseExtras(ctx, s); err != nil {
		return err
	}

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
	intype := ""
	outtype := ""
	if s := l.Schema; s != nil {
		if !s.IsResolved() {
			rs, err := s.Resolve(ctx.Schema)
			if err != nil {
				return "", err
			}
			s = rs
		}
		intype = "interface{}"
		if t, ok := ctx.RequestPayloadType[name]; ok {
			intype = t
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
		if t, ok := ctx.ResponsePayloadType[name]; ok {
			outtype = t
		}
	}

	buf := bytes.Buffer{}
	fmt.Fprintf(&buf, `func (c *Client) %s(`, name)
	if intype != "" {
		buf.WriteString("in ")
		if genutil.LooksLikeStruct(intype) {
			buf.WriteRune('*')
		}
		buf.WriteString(intype)
	}
	buf.WriteRune(')')

	if outtype == "" {
		buf.WriteString(`(err error) {`)
	} else {
		prefix := ""
		if genutil.LooksLikeStruct(outtype) {
			prefix = "*"
		}

		fmt.Fprintf(&buf, `(ret %s%s, err error) {`, prefix, outtype)
	}

	buf.WriteString("\nif pdebug.Enabled {")
	fmt.Fprintf(&buf, "\ng := pdebug.Marker(%s).BindError(&err)", strconv.Quote("client."+name))
	buf.WriteString("\ndefer g.End()")
	buf.WriteString("\n}")

	errbuf := bytes.Buffer{}
	errbuf.WriteString("\nif err != nil {")
	if outtype == "" {
		errbuf.WriteString("\nreturn err")
	} else {
		errbuf.WriteString("\nreturn nil, err")
	}
	errbuf.WriteString("\n}")
	errout := errbuf.String()

	fmt.Fprintf(&buf, "\n"+`u, err := url.Parse(c.Endpoint + %s)`, strconv.Quote(l.Path()))
	buf.WriteString(errout)

	method := strings.ToLower(l.Method)
	if method == "" {
		method = "get"
	}
	if _, ok := ctx.RequestPayloadType[name]; ok {
		if method == "get" {
			buf.WriteString("\nbuf, err := urlenc.Marshal(in)")
			buf.WriteString(errout)
			buf.WriteString("\nu.RawQuery = string(buf)")
		} else {
			buf.WriteString("\n" + `buf := bytes.Buffer{}`)
			buf.WriteString("\n" + `err = json.NewEncoder(&buf).Encode(in)`)
			buf.WriteString(errout)
		}
	}

	switch method {
	case "get":
		buf.WriteString("\nif pdebug.Enabled {")
		fmt.Fprintf(&buf, "\npdebug.Printf(%s, u.String())", strconv.Quote("GET to %s"))
		buf.WriteString("\n}")
		buf.WriteString("\n" + `res, err := c.Client.Get(u.String())`)
		buf.WriteString(errout)
	case "post":
		buf.WriteString("\nif pdebug.Enabled {")
		fmt.Fprintf(&buf, "\npdebug.Printf(%s, u.String())", strconv.Quote("POST to %s"))
		buf.WriteString("\n" + `pdebug.Printf("%s", buf.String())`)
		buf.WriteString("\n}")
		buf.WriteString("\n" + `res, err := c.Client.Post(u.String(), "application/json", &buf)`)
		buf.WriteString(errout)
	}
	buf.WriteString("\nif res.StatusCode != http.StatusOK {")
	buf.WriteString("\nreturn ")
	if outtype != "" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("fmt.Errorf(`Invalid response: '%s'`, res.Status)")
	buf.WriteString("\n}")
	if outtype == "" {
		buf.WriteString("\nreturn nil")
	} else {
		buf.WriteString("\nvar body io.Reader = res.Body")
		buf.WriteString("\nif pdebug.Enabled {")
		buf.WriteString("\njsbuf, err := ioutil.ReadAll(res.Body)")
		buf.WriteString("\nif err != nil {")
		buf.WriteString("\n" + `pdebug.Printf("failed to read respons buffer: %s", err)`)
		buf.WriteString("\n} else {")
		buf.WriteString("\n" + `pdebug.Printf("response buffer: %s", jsbuf)`)
		buf.WriteString("\n}")
		buf.WriteString("\nbody = bytes.NewReader(jsbuf)")
		buf.WriteString("\n}")

		buf.WriteString("\n\nvar payload ")
		buf.WriteString(outtype)
		buf.WriteString("\nerr = json.NewDecoder(body).Decode(")
		buf.WriteString("&")
		buf.WriteString("payload)")
		buf.WriteString(errout)
		buf.WriteString("\nreturn ")
		if genutil.LooksLikeStruct(outtype) {
			buf.WriteString("&")
		}
		buf.WriteString("payload, nil")
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
		fn := filepath.Join(ctx.Dir, "client", "client.go")
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

	imports := []string{"github.com/lestrrat/go-pdebug", "github.com/lestrrat/go-urlenc"}
	if l := ctx.ClientHints.Imports; len(l) > 0 {
		imports = append(imports, l...)
	}

	genutil.WriteImports(
		&buf,
		[]string{"bytes", "encoding/json", "fmt", "io", "io/ioutil", "net/http", "net/url"},
		imports,
	)

	buf.WriteString(`
var _ = bytes.MinRead
var _ = json.Decoder{}

type Client struct {
	Client *http.Client
	Endpoint string
}

func New(s string) *Client {
	return &Client{
		Client: &http.Client{},
		Endpoint: s,
	}
}

`)

	// for each endpoint, create a method that accepts
	for _, methodName := range ctx.MethodNames {
		method := ctx.Methods[methodName]
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
