package nethttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-jsval/builder"
	"github.com/lestrrat/go-pdebug"
)

type Builder struct {
	AppPkg       string
	ValidatorPkg string
}

type genctx struct {
	apppkg            string
	schema            *hschema.HyperSchema
	clientpkg         string
	validatorpkg      string
	methods           map[string]string
	methodPayloadType map[string]string
	methodNames       []string
	methodValidators  map[string]*jsval.JSVal
	pathToMethods     map[string]string
}

func New() *Builder {
	return &Builder{
		AppPkg:       "app",
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
		schema:            s,
		methodNames:       make([]string, len(s.Links)),
		apppkg:            b.AppPkg,
		clientpkg:         "client",
		validatorpkg:      b.ValidatorPkg,
		methods:           make(map[string]string),
		methodPayloadType: make(map[string]string),
		methodValidators:  make(map[string]*jsval.JSVal),
		pathToMethods:     make(map[string]string),
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

var wsrx = regexp.MustCompile(`\s+`)

func title2name(s string) string {
	buf := bytes.Buffer{}
	for _, p := range wsrx.Split(s, -1) {
		buf.WriteString(strings.ToUpper(p[:1]))
		buf.WriteString(p[1:])
	}
	return buf.String()
}

func parse(ctx *genctx, s *hschema.HyperSchema) error {
	for i, link := range s.Links {
		methodName := title2name(link.Title)

		// Got to do this first, because validators are used in makeMethod()
		if link.Schema != nil {
			v, err := makeValidator(ctx, link)
			if err != nil {
				return err
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.methodValidators[methodName] = v
		}
		ctx.methodPayloadType[methodName] = "interface{}"
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
		ctx.methods[methodName] = makeMethod(ctx, methodName, link)
	}

	sort.Strings(ctx.methodNames)
	return nil
}

func makeMethod(ctx *genctx, name string, l *hschema.Link) string {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, `func http%s(w http.ResponseWriter, r *http.Response) {`+"\n", name)
	if m := l.Method; m != "" {
		buf.WriteString("\tif strings.ToLower(r.Method) != `")
		fmt.Fprintf(&buf, "%s", strings.ToLower(m))
		buf.WriteString("` {\n\t\thttp.Error(w, `Not Found`, http.StatusNotFound)\n\t}\n")
	}

	if v := ctx.methodValidators[name]; v != nil {
		payloadType := ctx.methodPayloadType[name]
		fmt.Fprintf(&buf, "\n\tvar payload %s", payloadType)
		buf.WriteString("\n\tif err := json.NewDecoder(r.Body).Decode(&payload); err != nil {")
		buf.WriteString("\n\t\thttp.Error(w, `Invalid input`, http.StatusInternalServerError)")
		buf.WriteString("\n\t\treturn")
		buf.WriteString("\n\t}")
		fmt.Fprintf(&buf, "\n\n\tif err := %s.%s.Validate(payload); err != nil {", ctx.validatorpkg, v.Name)
		buf.WriteString("\n\t\thttp.Error(w, `Invalid input`, http.StatusInternalServerError)")
		buf.WriteString("\n\t\treturn")
		buf.WriteString("\n\t}")
	}

	fmt.Fprintf(&buf, "\n\tdo%s(context.Background(), w, r, payload)", name)
	buf.WriteString("\n}\n")
	return buf.String()
}

func makeValidator(ctx *genctx, l *hschema.Link) (*jsval.JSVal, error) {
	b := builder.New()
	v, err := b.BuildWithCtx(l.Schema, ctx.schema)
	if err != nil {
		return nil, err
	}

	return v, nil
}

func generateFile(ctx *genctx, fn string, cb func(io.Writer, *genctx) error) error {
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
		fn := filepath.Join(ctx.apppkg, ctx.validatorpkg, fmt.Sprintf("%s.go", ctx.validatorpkg))
		if err := generateFile(ctx, fn, generateValidatorCode); err != nil {
			return err
		}
	}

	return nil
}

func generateServerCode(out io.Writer, ctx *genctx) error {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, "package %s\n\n", ctx.apppkg)

	genutil.WriteImports(
		&buf,
		[]string{
			"net/http",
			"strings",
		},
		[]string{
			"github.com/gorilla/mux",
			"golang.org/x/context",
		},
	)

	buf.WriteString(`
type Server struct {
	*mux.Router
}

func New() *Server {
	s := &Server{
		Router: mux.NewRouter(),
	}
	s.SetupRoutes()
	return s
}

`)

	for _, methodName := range ctx.methodNames {
		buf.WriteString(ctx.methods[methodName])
		buf.WriteString("\n")
	}

	buf.WriteString("func (s *Server) SetupRoutes() {")
	buf.WriteString("\n\tr := s.Router")
	paths := make([]string, 0, len(ctx.pathToMethods))
	for path := range ctx.pathToMethods {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		method := ctx.pathToMethods[path]
		fmt.Fprintf(&buf, "\n\tr.HandleFunc(`%s`, %s)", path, method)
	}
	buf.WriteString("\n}\n")

	if _, err := buf.WriteTo(out); err != nil {
		return err
	}
	return nil
}

func generateValidatorCode(out io.Writer, ctx *genctx) error {
	g := jsval.NewGenerator()
	validators := make([]*jsval.JSVal, 0, len(ctx.methodValidators))
	for _, v := range ctx.methodValidators {
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

	if _, err := buf.WriteTo(out); err != nil {
		return err
	}

	return nil
}
