package nethttp

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
	"time"

	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-pdebug"
)

type Builder struct {
	AppPkg       string
	Overwrite    bool
	PkgPath      string
	ValidatorPkg string
}

type genctx struct {
	apppkg              string
	schema              *hschema.HyperSchema
	clientpkg           string
	validatorpkg        string
	methods             map[string]string
	methodNames         []string
	methodWrappers      map[string][]string
	overwrite           bool
	pathToMethods       map[string]string
	pkgpath             string
	requestPayloadType  map[string]string
	requestValidators   map[string]*jsval.JSVal
	responsePayloadType map[string]string
	responseValidators  map[string]*jsval.JSVal
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
		schema:              s,
		methodNames:         make([]string, len(s.Links)),
		apppkg:              b.AppPkg,
		methods:             make(map[string]string),
		methodWrappers:      make(map[string][]string),
		overwrite:           b.Overwrite,
		pathToMethods:       make(map[string]string),
		pkgpath:             b.PkgPath,
		requestPayloadType:  make(map[string]string),
		requestValidators:   make(map[string]*jsval.JSVal),
		responseValidators:  make(map[string]*jsval.JSVal),
		responsePayloadType: make(map[string]string),
		validatorpkg:        b.ValidatorPkg,
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
		ctx.requestPayloadType[methodName] = "interface{}"

		// Got to do this first, because validators are used in makeMethod()
		if ls := link.Schema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.schema)
				if err != nil {
					return err
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.schema)
			if err != nil {
				return err
			}
			if gt, ok := ls.Extras["gotype"]; ok {
				ctx.requestPayloadType[methodName] = gt.(string)
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.requestValidators[methodName] = v
		}
		if ls := link.TargetSchema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.schema)
				if err != nil {
					return err
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.schema)
			if err != nil {
				return err
			}
			if gt, ok := ls.Extras["gotype"]; ok {
				ctx.responsePayloadType[methodName] = gt.(string)
			}
			v.Name = fmt.Sprintf("HTTP%sResponse", methodName)
			ctx.responseValidators[methodName] = v
		}

		if ls := link.Schema; ls != nil {
			if pdebug.Enabled {
				pdebug.Printf("checking extras for %s: %#v", link.Path(), ls.Extras)
			}
			if gt, ok := ls.Extras["gotype"]; ok {
				ctx.requestPayloadType[methodName] = gt.(string)
			}
		}

		ctx.methodNames[i] = methodName
		ctx.pathToMethods[link.Path()] = methodName
		methodBody, err := makeMethod(ctx, methodName, link)
		if err != nil {
			return err
		}
		ctx.methods[methodName] = methodBody
		if m := link.Extras; m != nil {
			w, ok := m["gowrapper"]
			if ok {
				switch w.(type) {
				case string:
					ctx.methodWrappers[methodName] = []string{w.(string)}
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
						ctx.methodWrappers[methodName] = rl
					}
				default:
					return errors.New("wrapper must be a string, or an array of strings")
				}
			}
		}
	}

	sort.Strings(ctx.methodNames)
	return nil
}

func makeMethod(ctx *genctx, name string, l *hschema.Link) (string, error) {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, `func http%s(w http.ResponseWriter, r *http.Request) {`+"\n", name)
	method := strings.ToLower(l.Method)
	if method == "" {
		method = "get"
	}
	buf.WriteString("if strings.ToLower(r.Method) != `")
	fmt.Fprintf(&buf, "%s", method)
	buf.WriteString("` {\nhttp.Error(w, `Not Found`, http.StatusNotFound)\n}\n")

	if v := ctx.requestValidators[name]; v != nil {
		payloadType := ctx.requestPayloadType[name]
		if method == "get" {
			// If this is a get request, then we'd have to assemble
			// the incoming data from r.Form
			if payloadType == "interface{}" {
				buf.WriteString("\nif err := r.ParseForm(); err != nil {")
				buf.WriteString("\nhttp.Error(w, `Failed to process query/post form`, http.StatusInternalServerError)")
				buf.WriteString("\nreturn")
				buf.WriteString("\n}")
				buf.WriteString("\npayload := make(map[string]interface{})")
				for k, v := range l.Schema.Properties {
					if !v.IsResolved() {
						rv, err := v.Resolve(ctx.schema)
						if err != nil {
							return "", err
						}
						v = rv
					}

					if len(v.Type) != 1 {
						return "", errors.New("'" + name + "' can't handle input parameters unless the type contains exactly 1 type")
					}

					qk := strconv.Quote(k)
					buf.WriteString("\n{")
					switch v.Type[0] {
					case schema.IntegerType:
						fmt.Fprintf(&buf, "\nv, err := getInteger(r.Form, %s)", qk)
						fmt.Fprintf(&buf, `
if err != nil {
	http.Error(w, "Invalid parameter " + %s, http.StatusInternalServerError)
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
			}
		} else {
			buf.WriteString("\nvar payload ")
			if genutil.LooksLikeStruct(payloadType) {
				buf.WriteRune('*')
			}
			buf.WriteString(payloadType)
			buf.WriteString("\nif err := json.NewDecoder(r.Body).Decode(payload); err != nil {")
			buf.WriteString("\nhttp.Error(w, `Invalid input`, http.StatusInternalServerError)")
			buf.WriteString("\nreturn")
			buf.WriteString("\n}")
		}
		fmt.Fprintf(&buf, "\n\nif err := %s.%s.Validate(payload); err != nil {", ctx.validatorpkg, v.Name)
		buf.WriteString("\nhttp.Error(w, `Invalid input`, http.StatusInternalServerError)")
		buf.WriteString("\nreturn")
		buf.WriteString("\n}")
	}

	fmt.Fprintf(&buf, "\ndo%s(context.Background(), w, r", name)
	if _, ok := ctx.requestValidators[name]; ok {
		buf.WriteString(`, payload`)
	}
	buf.WriteString(`)`)
	buf.WriteString("\n}\n")

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

func generateFiles(ctxif interface{}) error {
	ctx, ok := ctxif.(*genctx)
	if !ok {
		return errors.New("expected genctx type")
	}

	{
		fn := filepath.Join(ctx.apppkg, fmt.Sprintf("%s.go", ctx.apppkg))
		if err := generateFile(ctx, fn, generateServerCode); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.apppkg, "handlers.go")
		if err := generateFile(ctx, fn, generateStubHandlerCode); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.apppkg, ctx.validatorpkg, fmt.Sprintf("%s.go", ctx.validatorpkg))
		if err := generateFile(ctx, fn, generateValidatorCode); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.apppkg, "cmd", ctx.apppkg, fmt.Sprintf("%s.go", ctx.apppkg))
		if err := generateFile(ctx, fn, generateExecutableCode); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.apppkg, "interface.go")
		if err := generateFile(ctx, fn, generateDataCode); err != nil {
			return err
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
		[]string{ctx.pkgpath, "github.com/jessevdk/go-flags"},
	)

	buf.WriteString(`type options struct {` + "\n")
	buf.WriteString(`Listen string ` + "`" + `short:"l" long:"listen" default:":8080" description:"Listen address"` + "`\n")
	buf.WriteString("}\n")
	buf.WriteString(`func main() { os.Exit(_main()) }` + "\n")
	buf.WriteString(`func _main() int {
	var opts options
	if _, err := flags.Parse(&opts); err != nil {
		log.Printf("%s", err)
		return 1
	}
`)
	buf.WriteString(`log.Printf("Server listening on %s", opts.Listen)` + "\n")
	fmt.Fprintf(&buf, `if err := %s.Run(opts.Listen); err != nil {`+"\n", ctx.apppkg)
	buf.WriteString(` log.Printf("%s", err)
		return 1
	}
	return 0
}`)

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("Failed to cleanup Go code (probably a syntax error). Generating file anyway")
		if _, err := buf.WriteTo(out); err != nil {
			return err
		}
		return nil
	}

	if _, err := out.Write(fsrc); err != nil {
		return err
	}
	return nil
}

func generateStubHandlerCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "package %s\n\n", ctx.apppkg)

	genutil.WriteImports(
		&buf,
		[]string{
			"net/http",
		},
		[]string{
			"golang.org/x/net/context",
		},
	)

	for _, methodName := range ctx.methodNames {
		payloadType := ctx.requestPayloadType[methodName]

		fmt.Fprintf(&buf, "\nfunc do%s(ctx context.Context, w http.ResponseWriter, r *http.Request", methodName)
		if _, ok := ctx.requestValidators[methodName]; ok {
			buf.WriteString(`, payload `)
			if genutil.LooksLikeStruct(payloadType) {
				buf.WriteRune('*')
			}
			buf.WriteString(payloadType)
		}
		buf.WriteString(") {")
		buf.WriteString("\n}\n")
	}

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("Failed to cleanup Go code (probably a syntax error). Generating file anyway")
		if _, err := buf.WriteTo(out); err != nil {
			return err
		}
		return nil
	}

	if _, err := out.Write(fsrc); err != nil {
		return err
	}
	return nil
}

func generateServerCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "// DO NOT EDIT. Automatically generated by hsup at %s\n", time.Now().Format(time.RFC1123))
	fmt.Fprintf(&buf, "package %s\n\n", ctx.apppkg)

	genutil.WriteImports(
		&buf,
		[]string{
			"encoding/json",
			"net/http",
			"net/url",
			"strconv",
			"strings",
		},
		[]string{
			filepath.Join(ctx.pkgpath, "validator"),
			"github.com/gorilla/mux",
			"golang.org/x/net/context",
		},
	)

	buf.WriteString(`
type Server struct {
	*mux.Router
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

`)

	for _, methodName := range ctx.methodNames {
		buf.WriteString(ctx.methods[methodName])
		buf.WriteString("\n")
	}

	buf.WriteString("func (s *Server) SetupRoutes() {")
	buf.WriteString("\nr := s.Router")
	paths := make([]string, 0, len(ctx.pathToMethods))
	for path := range ctx.pathToMethods {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		method := ctx.pathToMethods[path]

		fmt.Fprintf(&buf, "\nr.HandleFunc(`%s`, ", path)
		for _, w := range ctx.methodWrappers[method] {
			fmt.Fprintf(&buf, "%s(", w)
		}
		fmt.Fprintf(&buf, "http%s)", method)
		for range ctx.methodWrappers[method] {
			buf.WriteString(")")
		}
	}
	buf.WriteString("\n}\n")

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("Failed to cleanup Go code (probably a syntax error). Generating file anyway")
		if _, err := buf.WriteTo(out); err != nil {
			return err
		}
		return nil
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
		log.Printf("Failed to cleanup Go code (probably a syntax error). Generating file anyway")
		if _, err := buf.WriteTo(out); err != nil {
			return err
		}
		return nil
	}

	if _, err := out.Write(fsrc); err != nil {
		return err
	}

	return nil
}

func generateDataCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}
	fmt.Fprintf(&buf, `package %s`+"\n\n", ctx.apppkg)

	types := make(map[string]struct{})
	for _, t := range ctx.requestPayloadType {
		types[t] = struct{}{}
	}
	for _, t := range ctx.responsePayloadType {
		types[t] = struct{}{}
	}

	for t := range types {
		if genutil.LooksLikeStruct(t) {
			fmt.Fprintf(&buf, "type %s struct {}\n", t)
		}
	}

	fsrc, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("Failed to cleanup Go code (probably a syntax error). Generating file anyway")
		if _, err := buf.WriteTo(out); err != nil {
			return err
		}
		return nil
	}

	if _, err := out.Write(fsrc); err != nil {
		return err
	}

	return nil
}
