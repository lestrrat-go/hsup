package nethttp

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jessevdk/go-flags"
	"github.com/lestrrat/go-hsup"
	"github.com/lestrrat/go-hsup/ext"
	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-hsup/internal/parser"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsschema"
	"github.com/pkg/errors"
)

type Builder struct {
	AppPkg       string
	ClientPkg    string
	CLISchema    string
	Dir          string
	Overwrite    bool
	PkgPath      string
	ValidatorPkg string
}

type serverHints struct {
	Imports []string
}

type genctx struct {
	*parser.Result
	AppPkg       string
	ClientPkg    string
	CLISchema    string
	Dir          string
	Overwrite    bool
	PkgPath      string
	ServerHints  serverHints
	ValidatorPkg string
}

type options struct {
	CLISchema string `long:"clischema"`
}

func Process(opts hsup.Options) error {
	var localopts options
	if _, err := flags.ParseArgs(&localopts, opts.Args); err != nil {
		return errors.Wrap(err, "failed to parse command line arguments")
	}

	b := New()
	b.Dir = opts.Dir
	b.AppPkg = opts.AppPkg
	b.PkgPath = opts.PkgPath
	b.Overwrite = opts.Overwrite
	b.CLISchema = localopts.CLISchema
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}

	return nil
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
		return errors.Wrap(err, "failed to read JSON Hyper Schema file")
	}
	return errors.Wrap(b.Process(s), "failed to process the JSON Hyper Schema")
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
		ClientPkg:    b.ClientPkg,
		CLISchema:    b.CLISchema,
		Dir:          b.Dir,
		Overwrite:    b.Overwrite,
		PkgPath:      b.PkgPath,
		ValidatorPkg: b.ValidatorPkg,
	}

	if err := parse(&ctx, s); err != nil {
		return errors.Wrap(err, "failed to parse schema")
	}

	if err := generateFiles(&ctx); err != nil {
		return errors.Wrap(err, "failed to generate files")
	}

	log.Printf(" <=== All files generated")
	return nil
}

func parseServerHints(ctx *genctx, m map[string]interface{}) error {
	if v, ok := m["imports"]; ok {
		switch v.(type) {
		case []interface{}:
		default:
			return errors.New("invalid value type for imports: expected []interface{}")
		}

		l := v.([]interface{})
		ctx.ServerHints.Imports = make([]string, len(l))
		for i, n := range l {
			switch n.(type) {
			case string:
			default:
				return errors.New("invalid value type for elements in imports: expected string")
			}
			ctx.ServerHints.Imports[i] = n.(string)
		}
	}
	return nil
}

func parseExtras(ctx *genctx, s *hschema.HyperSchema) error {
	for k, v := range s.Extras {
		switch k {
		case "hsup.server":
			switch v.(type) {
			case map[string]interface{}:
			default:
				return errors.New("invalid value type for hsup.server: expected map[string]interface{}")
			}

			if err := parseServerHints(ctx, v.(map[string]interface{})); err != nil {
				return errors.Wrap(err, "failed to parse server hints")
			}
		}
	}
	return nil
}

func parse(ctx *genctx, s *hschema.HyperSchema) error {
	pres, err := parser.Parse(s)
	if err != nil {
		return errors.Wrap(err, "failed to parse schema")
	}
	ctx.Result = pres

	if err := parseExtras(ctx, s); err != nil {
		return errors.Wrap(err, "failed to parse extras")
	}

	for _, link := range s.Links {
		methodName := genutil.TitleToName(link.Title)
		methodBody, err := makeMethod(ctx, methodName, link)
		if err != nil {
			return errors.Wrap(err, "failed to make method '"+methodName+"'")
		}
		ctx.Methods[methodName] = methodBody
		if m := link.Extras; len(m) > 0 {
			w, ok := m[ext.WrapperKey]
			if ok {
				switch w.(type) {
				case string:
					ctx.MethodWrappers[methodName] = []string{w.(string)}
				case []interface{}:
					wl := w.([]interface{})
					if len(wl) > 0 {
						rl := make([]string, len(wl))
						for i, ws := range wl {
							switch ws.(type) {
							case string:
								rl[i] = ws.(string)
							default:
								return errors.New("wrapper elements must be strings")
							}
						}
						ctx.MethodWrappers[methodName] = rl
					}
				default:
					return errors.New("wrapper must be a string, or an array of strings")
				}
			}
		}
	}

	return nil
}

func makeMethod(ctx *genctx, name string, l *hschema.Link) (string, error) {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, `func http%s(ctx context.Context, w http.ResponseWriter, r *http.Request) {`, name)
	buf.WriteString("\nif pdebug.Enabled {")
	fmt.Fprintf(&buf, "\ng := pdebug.Marker(%s)", strconv.Quote("http"+name))
	buf.WriteString("\ndefer g.End()")
	buf.WriteString("\n}")

	method := strings.ToLower(l.Method)
	if method == "" {
		method = "get"
	}
	fmt.Fprintf(&buf, "\nif strings.ToLower(r.Method) != `%s` {", method)
	fmt.Fprintf(&buf, "\n"+`w.Header().Set("Allow", %s)`, strconv.Quote(method))
	fmt.Fprintf(&buf, "\nhttpError(w, `Method was ` + r.Method + `, expected %s`, http.StatusNotFound, nil)", method)
	buf.WriteString("\nreturn")
	buf.WriteString("\n}\n")

	payloadType := ctx.RequestPayloadType[name]

	if v := ctx.RequestValidators[name]; v != nil {
		// If this is a get request, then we'd have to assemble
		// the incoming data from r.Form
		if method == "get" {
			switch payloadType {
			case "interface{}", "map[string]interface{}":
				buf.WriteString("\nif err := r.ParseForm(); err != nil {")
				buf.WriteString("\nhttpError(w, `Failed to process query/post form`, http.StatusInternalServerError, nil)")
				buf.WriteString("\nreturn")
				buf.WriteString("\n}")
				buf.WriteString("\npayload := make(map[string]interface{})")

				pnames := make([]string, 0, len(l.Schema.Properties))
				for k := range l.Schema.Properties {
					pnames = append(pnames, k)
				}
				sort.Strings(pnames)

				for _, k := range pnames {
					v := l.Schema.Properties[k]
					if !v.IsResolved() {
						rv, err := v.Resolve(ctx.Schema)
						if err != nil {
							return "", errors.Wrap(err, "failed to resolve schema")
						}
						v = rv
					}

					if len(v.Type) != 1 {
						return "", fmt.Errorf("'%s.%s' can't handle input parameters unless the type contains exactly 1 type (got: %v)", name, k, v.Type)
					}

					qk := strconv.Quote(k)
					buf.WriteString("\n{")
					switch v.Type[0] {
					case schema.IntegerType:
						fmt.Fprintf(&buf, "\nv, err := getInteger(r.Form, %s)", qk)
						fmt.Fprintf(&buf, `
if err != nil {
	httpError(w, "Invalid parameter " + %s, http.StatusInternalServerError, err)
	return
}
`, strconv.Quote(k))
					case schema.StringType:
						fmt.Fprintf(&buf, "\nv := r.Form[%s]", qk)
					}
					fmt.Fprintf(&buf, `
switch len(v) {
case 0:
case 1:
	payload[%s] = v[0]
default:
	payload[%s] = v
}
}
`, qk, qk)
				}
			default:
				buf.WriteString("\nvar payload ")
				buf.WriteString(strings.TrimPrefix(payloadType, ctx.AppPkg+"."))
				buf.WriteString("\nif err := urlenc.Unmarshal([]byte(r.URL.RawQuery), &payload); err != nil {")
				buf.WriteString("\nhttpError(w, `Failed to parse url query string`, http.StatusInternalServerError, err)")
				buf.WriteString("\nreturn")
				buf.WriteString("\n}")
			}
		} else {
			buf.WriteString("\nvar payload ")
			buf.WriteString(strings.TrimPrefix(payloadType, ctx.AppPkg+"."))

			buf.WriteString("\njsonbuf := getTransportJSONBuffer()")
			buf.WriteString("\ndefer releaseTransportJSONBuffer(jsonbuf)")
			buf.WriteString("\nif _, err := io.Copy(jsonbuf, io.LimitReader(r.Body, MaxPostSize)); err != nil {")
			buf.WriteString("\nhttpError(w, `Failed to read request body`, http.StatusInternalServerError, err)")
			buf.WriteString("\n}")
			buf.WriteString("\ndefer r.Body.Close()")
			buf.WriteString("\nif pdebug.Enabled {")
			buf.WriteString("\npdebug.Printf(`-----> %s`, jsonbuf.Bytes())")
			buf.WriteString("\n}")
			buf.WriteString("\nif err := json.Unmarshal(jsonbuf.Bytes(), &payload); err != nil {")
			buf.WriteString("\nhttpError(w, `Invalid JSON input`, http.StatusInternalServerError, err)")
			buf.WriteString("\nreturn")
			buf.WriteString("\n}")
		}

		fmt.Fprintf(&buf, "\n\nif err := %s.%s.Validate(&payload); err != nil {", ctx.ValidatorPkg, v.Name)
		buf.WriteString("\nhttpError(w, `Invalid input (validation failed)`, http.StatusInternalServerError, err)")
		buf.WriteString("\nreturn")
		buf.WriteString("\n}")
	}

	fmt.Fprintf(&buf, "\ndo%s(ctx, w, r", name)
	if _, ok := ctx.RequestValidators[name]; ok {
		buf.WriteString(`, payload`)
	}
	buf.WriteString(`)`)
	buf.WriteString("\n}\n")

	return buf.String(), nil
}

func generateFile(ctx *genctx, fn string, cb func(io.Writer, *genctx) error, forceOverwrite bool) error {
	if _, err := os.Stat(fn); err == nil {
		if !ctx.Overwrite {
			log.Printf(" - File '%s' already exists. Skipping", fn)
			return nil
		}
		if forceOverwrite {
			log.Printf(" * File '%s' already exists. Overwriting", fn)
		} else {
			log.Printf(" - File '%s' already exists. This file cannot be overwritten, skipping", fn)
			return nil
		}
	}

	log.Printf(" + Generating file '%s'", fn)
	f, err := genutil.CreateFile(fn)
	if err != nil {
		return errors.Wrap(err, "failed to generate file '"+fn+"'")
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
	}()

	if err := cb(f, ctx); err != nil {
		f.Close()
		closed = true
		os.Remove(fn)
		return errors.Wrap(err, "callback failed")
	}
	return nil
}

func generateFiles(ctxif interface{}) error {
	ctx, ok := ctxif.(*genctx)
	if !ok {
		return errors.New("expected genctx type")
	}

	// these files are expected to be completely under control by the
	// hsup system, so get forcefully overwritten
	sysfiles := map[string]func(io.Writer, *genctx) error{
		filepath.Join(ctx.Dir, fmt.Sprintf("%s_hsup.go", ctx.AppPkg)): generateServerCode,
	}
	for fn, cb := range sysfiles {
		if err := generateFile(ctx, fn, cb, true); err != nil {
			return errors.Wrap(err, "failed to generate file '"+fn+"'")
		}
	}

	// these files are expected to be modified by the author, so do
	// not get forcefully overwritten
	userfiles := map[string]func(io.Writer, *genctx) error{
		filepath.Join(ctx.Dir, "cmd", ctx.AppPkg, fmt.Sprintf("%s.go", ctx.AppPkg)): generateExecutableCode,
		filepath.Join(ctx.Dir, "handlers.go"):                                       generateStubHandlerCode,
		filepath.Join(ctx.Dir, "interface.go"):                                      generateDataCode,
		filepath.Join(ctx.Dir, "client_test.go"):                                    generateTestCode,
	}
	for fn, cb := range userfiles {
		if err := generateFile(ctx, fn, cb, false); err != nil {
			return errors.Wrap(err, "failed to generate file '"+fn+"'")
		}
	}

	return nil
}

func generateExecutableCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}
	buf.WriteString(`package main` + "\n\n")
	genutil.WriteImports(
		&buf,
		[]string{"log", "os"},
		[]string{ctx.PkgPath, "github.com/jessevdk/go-flags", "github.com/pkg/errors"},
	)

	f := ctx.CLISchema
	if f == "" {
		buf.WriteString(`type options struct {` + "\n")
		buf.WriteString(`Listen string ` + "`" + `short:"l" long:"listen" default:":8080" description:"Listen address"` + "`\n")
		buf.WriteString("}\n")
	} else {
		s, err := schema.ReadFile(f)
		if err != nil {
			return errors.Wrap(err, "failed to read CLI schema file '"+f+"'")
		}

		buf.WriteString(`type options struct {` + "\n")
		for name, pschema := range s.Properties {
			var typ string
			typv, ok := pschema.Extras[ext.TypeKey]
			if !ok {
				switch pschema.Type[0] {
				case schema.IntegerType:
					typ = "int"
				case schema.StringType:
					typ = "string"
				case schema.BooleanType:
					typ = "bool"
				case schema.NumberType:
					typ = "float64"
				default:
					return errors.New("complex types cannot be automatically deduced. consider using 'hsup.type' key")
				}
			} else {
				typ, ok = typv.(string)
				if !ok {
					return errors.New("could not determine parameter type")
				}
			}
			buf.WriteString(genutil.TitleToName(name))
			buf.WriteByte(' ')
			buf.WriteString(typ)
			buf.WriteByte(' ')
			buf.WriteString("`" + `long:"` + name + `"` + "`")
		}
		buf.WriteString("}\n")
	}

	buf.WriteString(`
type ignorableError interface {
	Ignorable() bool
}

func isIgnorableError(err error) bool {
	if i, ok := err.(ignorableError); ok {
		return i.Ignorable()
	}
	return false
}

func main() {
	status := 1
	if err := _main(); err != nil {
		if !isIgnorableError(err) {
			println(err.Error())
		} else {
			status = 0
		}
	}
	os.Exit(status)
}

func _main() error {
	var opts options
	if _, err := flags.Parse(&opts); err != nil {
		return errors.Wrap(err, "failed to parse command line options")
	}
`)
	fmt.Fprintf(&buf, "if err := %s.ProcessOpts(&opts); err != nil {\n", ctx.AppPkg)
	buf.WriteString(`return errors.Wrap(err, "failed to process options")
	}

	log.Printf("Server listening on %s", opts.Listen)
`)
	fmt.Fprintf(&buf, `if err := %s.Run(opts.Listen); err != nil {`+"\n", ctx.AppPkg)
	buf.WriteString(` log.Printf("%s", err)
		return 1
	}
	return 0
}`)

	return genutil.WriteFmtCode(out, &buf)
}

func generateStubHandlerCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "package %s\n\n", ctx.AppPkg)

	genutil.WriteImports(
		&buf,
		[]string{
			"net/http",
		},
		[]string{
			"golang.org/x/net/context",
		},
	)

	for _, methodName := range ctx.MethodNames {
		payloadType := ctx.RequestPayloadType[methodName]
		payloadType = strings.TrimPrefix(payloadType, ctx.AppPkg+".")

		fmt.Fprintf(&buf, "\nfunc do%s(ctx context.Context, w http.ResponseWriter, r *http.Request", methodName)
		if _, ok := ctx.RequestValidators[methodName]; ok {
			buf.WriteString(`, payload `)
			buf.WriteString(payloadType)
		}
		buf.WriteString(") {")
		buf.WriteString("\n}\n")
	}

	return genutil.WriteFmtCode(out, &buf)
}

func generateServerCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "package %s\n\n", ctx.AppPkg)

	genutil.WriteDoNotEdit(&buf)

	imports := []string{
		"github.com/gorilla/mux",
		"github.com/lestrrat/go-pdebug",
		"github.com/lestrrat/go-urlenc",
		"golang.org/x/net/context",
	}

	if len(ctx.RequestValidators) > 0 || len(ctx.ResponseValidators) > 0 {
		imports = append(imports, filepath.Join(ctx.PkgPath, "validator"))
	}

	if len(ctx.ServerHints.Imports) > 0 {
		imports = append(imports, ctx.ServerHints.Imports...)
	}

	genutil.WriteImports(
		&buf,
		[]string{
			"bytes",
			"encoding/json",
			"io",
			"net/http",
			"net/url",
			"strconv",
			"strings",
			"sync",
		},
		imports,
	)

	buf.WriteString(`
const MaxPostSize = (1<<20)*2
var _ = json.Decoder{}
var _ = urlenc.Marshal
var transportJSONBufferPool = sync.Pool{
	New: allocTransportJSONBuffer,
}

func allocTransportJSONBuffer() interface {} {
	return &bytes.Buffer{}
}

func getTransportJSONBuffer() *bytes.Buffer {
	return transportJSONBufferPool.Get().(*bytes.Buffer)
}

func releaseTransportJSONBuffer(buf *bytes.Buffer) {
	buf.Reset()
	transportJSONBufferPool.Put(buf)
}

type Server struct {
	*mux.Router
}

// NewContext creates a cteonxt.Context object from the request.
// If you are using appengine, for example, you probably want to set this
// function to something that create a context, and then sets
// the appengine context to it so it can be referred to later.
var NewContext func(* http.Request) context.Context= func(r *http.Request) context.Context {
	return context.Background()
}

func Run(l string) error {
	return http.ListenAndServe(l, New())
}

func New() *Server {
	s := &Server{
		Router: mux.NewRouter(),
	}
	s.SetupRoutes()
	return s
}

var httpError func(http.ResponseWriter, string, int, error) = defaultHTTPError
func defaultHTTPError(w http.ResponseWriter, message string, st int, err error) {
  if pdebug.Enabled {
		if err == nil {
	    pdebug.Printf("HTTP Error %s", message)
		} else {
	    pdebug.Printf("HTTP Error %s: %s", message, err)
		}
  }
  http.Error(w, http.StatusText(st), st)
}

func getInteger(v url.Values, f string) ([]int64, error) {
	x, ok := v[f]
	if !ok {
		return nil, nil
	}

	ret := make([]int64, len(x))
	for i, e := range x {
		p, err := strconv.ParseInt(e, 10, 64)
		if err != nil {
			return nil, err
		}
		ret[i] = p
	}

	return ret, nil
}

func httpWithContext(h func(context.Context, http.ResponseWriter, *http.Request)) http.Handler {
	return func (w http.ResponseWriter, r *http.Request) {
		h(NewContext(r), w, r)
	}
}

`)

	for _, methodName := range ctx.MethodNames {
		buf.WriteString(ctx.Methods[methodName])
		buf.WriteString("\n")
	}

	buf.WriteString("func (s *Server) SetupRoutes() {")
	buf.WriteString("\nr := s.Router")
	paths := make([]string, 0, len(ctx.PathToMethods))
	for path := range ctx.PathToMethods {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		method := ctx.PathToMethods[path]

		fmt.Fprintf(&buf, "\nr.HandleFunc(`%s`, httpWithContext(", path)
		for _, w := range ctx.MethodWrappers[method] {
			fmt.Fprintf(&buf, "%s(", w)
		}
		fmt.Fprintf(&buf, "http%s", method)
		for range ctx.MethodWrappers[method] {
			buf.WriteString(")")
		}
		buf.WriteString("))")
	}
	buf.WriteString("\n}\n")

	return genutil.WriteFmtCode(out, &buf)
}

func generateDataCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}
	fmt.Fprintf(&buf, `package %s`+"\n\n", ctx.AppPkg)

	types := make(map[string]struct{})
	for _, t := range ctx.RequestPayloadType {
		types[t] = struct{}{}
	}
	for _, t := range ctx.ResponsePayloadType {
		types[t] = struct{}{}
	}

	for t := range types {
		if i := strings.IndexRune(t, '.'); i > -1 { // we have a qualified struct name?
			if prefix := t[:i+1]; prefix != "" {
				if prefix != ctx.AppPkg+"." {
					log.Printf(" * '%s' has a package name that's not the app package (%s != %s.)", t, prefix, ctx.AppPkg)
					continue
				}
			}
			t = strings.TrimPrefix(t, ctx.AppPkg+".")
			if genutil.LooksLikeStruct(t) {
				fmt.Fprintf(&buf, "type %s struct {}\n", t)
			}
		}
	}

	return genutil.WriteFmtCode(out, &buf)
}

func generateTestCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "package %s_test\n\n", ctx.AppPkg)

	imports := []string{
		ctx.PkgPath,
		filepath.Join(ctx.PkgPath, ctx.ClientPkg),
		"github.com/stretchr/testify/assert",
	}

	if len(ctx.ResponseValidators) > 0 {
		imports = append(imports, filepath.Join(ctx.PkgPath, ctx.ValidatorPkg))
	}

	genutil.WriteImports(
		&buf,
		[]string{
			"testing",
			"net/http/httptest",
		},
		imports,
	)

	for _, methodName := range ctx.MethodNames {
		fmt.Fprintf(&buf, "func Test%s(t *testing.T) {\n", methodName)
		fmt.Fprintf(&buf, "ts := httptest.NewServer(%s.New())\n", ctx.AppPkg)
		buf.WriteString(`defer ts.Close()

`)
		fmt.Fprintf(&buf, "cl := %s.New(ts.URL)\n", ctx.ClientPkg)

		if pt, ok := ctx.RequestPayloadType[methodName]; ok {
			buf.WriteString("var in ")
			if genutil.LooksLikeStruct(pt) {
				buf.WriteRune('*')
			}
			buf.WriteString(pt)
			buf.WriteString("\n")
		}
		if _, ok := ctx.ResponsePayloadType[methodName]; ok {
			buf.WriteString("res, ")
		}

		fmt.Fprintf(&buf, "err := cl.%s(", methodName)
		if _, ok := ctx.RequestPayloadType[methodName]; ok {
			buf.WriteString("in")
		}
		buf.WriteString(")\n")
		fmt.Fprintf(&buf, `if !assert.NoError(t, err, "%s should succeed") {`+"\n", methodName)
		buf.WriteString("return\n")
		buf.WriteString("}\n")
		if _, ok := ctx.ResponseValidators[methodName]; ok {
			fmt.Fprintf(&buf, `if !assert.NoError(t, %s.HTTP%sResponse.Validate(&res), "Validation should succeed") {`+"\n", ctx.ValidatorPkg, methodName)
			buf.WriteString("return\n}\n")
		}
		buf.WriteString("}\n\n")
	}

	return genutil.WriteFmtCode(out, &buf)
}
