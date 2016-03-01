package nethttp

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-jsval/builder"
	"github.com/lestrrat/go-pdebug"
)

type Builder struct {
	AppPkg       string
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
	pathToMethods      map[string]string
	requestValidators  map[string]*jsval.JSVal
	responseValidators map[string]*jsval.JSVal
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
		schema:             s,
		methodNames:        make([]string, len(s.Links)),
		apppkg:             b.AppPkg,
		clientpkg:          "client",
		validatorpkg:       b.ValidatorPkg,
		methods:            make(map[string]string),
		methodPayloadType:  make(map[string]string),
		pathToMethods:      make(map[string]string),
		requestValidators:  make(map[string]*jsval.JSVal),
		responseValidators: make(map[string]*jsval.JSVal),
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

		// Got to do this first, because validators are used in makeMethod()
		if s := link.Schema; s != nil {
			v, err := makeValidator(ctx, s)
			if err != nil {
				return err
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.requestValidators[methodName] = v
		}
		ctx.methodPayloadType[methodName] = "interface{}"
		if s := link.TargetSchema; s != nil {
			v, err := makeValidator(ctx, s)
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

	if v := ctx.requestValidators[name]; v != nil {
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

func makeValidator(ctx *genctx, s *schema.Schema) (*jsval.JSVal, error) {
	b := builder.New()
	v, err := b.BuildWithCtx(s, ctx.schema)
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
