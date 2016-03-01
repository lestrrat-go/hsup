package flavor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-jsval/builder"
	"github.com/lestrrat/go-pdebug"
)

type ctxNetHTTP struct {
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

func makeContextForNetHTTP() interface{} {
	return &ctxNetHTTP{
		apppkg:            "app",
		clientpkg:         "client",
		validatorpkg:      "validator",
		methods:           make(map[string]string),
		methodPayloadType: make(map[string]string),
		methodValidators:  make(map[string]*jsval.JSVal),
		pathToMethods:     make(map[string]string),
	}
}

var wsrx = regexp.MustCompile(`\s+`)

func title2method(s string) string {
	// inefficient as hell
	buf := bytes.Buffer{}
	buf.WriteString("http")
	for _, p := range wsrx.Split(s, -1) {
		buf.WriteString(strings.ToUpper(p[:1]))
		buf.WriteString(p[1:])
	}
	return buf.String()
}

func parseForNetHTTP(ctxif interface{}, s *hschema.HyperSchema) error {
	ctx, ok := ctxif.(*ctxNetHTTP)
	if !ok {
		return errors.New("expected ctxNetHTTP type")
	}

	ctx.schema = s
	ctx.methodNames = make([]string, len(s.Links))

	for i, link := range s.Links {
		methodName := title2method(link.Title)

		// Got to do this first, because validators are used in makeMethod()
		if link.Schema != nil {
			v, err := makeValidatorNetHTTP(ctx, link)
			if err != nil {
				return err
			}
			v.Name = fmt.Sprintf("%sRequest", methodName)
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
		ctx.methods[methodName] = makeMethodNetHTTP(ctx, methodName, link)
	}

	sort.Strings(ctx.methodNames)
	return nil
}

func makeMethodNetHTTP(ctx *ctxNetHTTP, name string, l *hschema.Link) string {
	buf := bytes.Buffer{}

	fmt.Fprintf(&buf, `func %s(w http.ResponseWriter, r *http.Response) {`+"\n", name)
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

	buf.WriteString("\n\thttp.Error(w, `Unimplemented`, http.StatusInternalServerError)")

	fmt.Fprint(&buf, "\n}\n")
	return buf.String()
}

func makeValidatorNetHTTP(ctx *ctxNetHTTP, l *hschema.Link) (*jsval.JSVal, error) {
	b := builder.New()
	v, err := b.BuildWithCtx(l.Schema, ctx.schema)
	if err != nil {
		return nil, err
	}

	return v, nil
}

func createFile(fn string) (*os.File, error) {
	dir := filepath.Dir(fn)
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	f, err := os.Create(fn)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func generateForNetHTTP(ctxif interface{}) error {
	ctx, ok := ctxif.(*ctxNetHTTP)
	if !ok {
		return errors.New("expected ctxNetHTTP type")
	}

	{
		fn := filepath.Join(ctx.apppkg, fmt.Sprintf("%s.go", ctx.apppkg))
		f, err := createFile(fn)
		if err != nil {
			return err
		}
		defer f.Close()

		if err := generateServerNetHTTP(f, ctx); err != nil {
			return err
		}
	}

	{
		fn := filepath.Join(ctx.validatorpkg, fmt.Sprintf("%s.go", ctx.validatorpkg))
		f, err := createFile(fn)
		if err != nil {
			return err
		}
		defer f.Close()

		if err := generateValidatorNetHTTP(f, ctx); err != nil {
			return err
		}
	}

	return nil
}

func generateServerNetHTTP(out io.Writer, ctx *ctxNetHTTP) error {
	buf := bytes.Buffer{}

	buf.WriteString("package apiserver\n\n")

	writeimports(
		&buf,
		[]string{
			"net/http",
			"strings",
		},
		[]string{
			"github.com/gorilla/mux",
		},
	)

	for _, methodName := range ctx.methodNames {
		buf.WriteString(ctx.methods[methodName])
		buf.WriteString("\n")
	}

	buf.WriteString("func setupRouter(r *mux.Router) {\n")
	paths := make([]string, 0, len(ctx.pathToMethods))
	for path := range ctx.pathToMethods {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		method := ctx.pathToMethods[path]
		fmt.Fprintf(&buf, "\tr.HandleFunc(`%s`, %s)\n", path, method)
	}
	buf.WriteString("}\n")

	if _, err := buf.WriteTo(out); err != nil {
		return err
	}
	return nil
}

func generateValidatorNetHTTP(out io.Writer, ctx *ctxNetHTTP) error {
  g := jsval.NewGenerator()
  validators := make([]*jsval.JSVal, 0, len(ctx.methodValidators))
  for _, v := range ctx.methodValidators {
    validators = append(validators, v)
  }

  buf := bytes.Buffer{}
  buf.WriteString("package " + ctx.validatorpkg + "\n\n")

  writeimports(
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
