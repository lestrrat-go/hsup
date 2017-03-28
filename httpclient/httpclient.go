package httpclient

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
	"github.com/pkg/errors"
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

type options struct {
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
	if err := b.ProcessFile(opts.Schema); err != nil {
		return err
	}
	return nil
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

	// If this is a multipart/form-data link, we need to add the potential
	// files. This will be specified as a map of strings
	var files []string
	if extv, ok := l.Extras[ext.MultipartFilesKey]; ok {
		listv, ok := extv.([]interface{})
		if !ok {
			return "", errors.Errorf("'%s' key must be a []string", ext.MultipartFilesKey)
		}
		files = make([]string, len(listv))
		for i, v := range listv {
			sv, ok := v.(string)
			if !ok {
				return "", errors.Errorf("'%s' key must be a []string", ext.MultipartFilesKey)
			}
			files[i] = sv
		}

		if intype != "" {
			buf.WriteString(", ")
		}

		buf.WriteString("files map[string]string")
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

	fmt.Fprintf(&buf, "\n"+`u, err := url.Parse(c.endpoint + %s)`, strconv.Quote(l.Path()))
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
			buf.WriteString("\nvar buf bytes.Buffer")
			if l.EncType == "multipart/form-data" {
				buf.WriteString("\nw := multipart.NewWriter(&buf)")
				buf.WriteString("\nvar jsbuf bytes.Buffer")
				buf.WriteString("\nerr = json.NewEncoder(&jsbuf).Encode(in)")
				buf.WriteString(errout)
				buf.WriteString("\nw.WriteField(\"payload\", jsbuf.String())")

				// files are specified outside of the schema, because they are not
				// to be validated
				for _, name := range files {
					fmt.Fprintf(&buf, "\nif fn, ok := files[%s]; ok {", strconv.Quote(name))
					fmt.Fprintf(&buf, "\nfw, err := w.CreateFormFile(%s, fn)", strconv.Quote(name))
					buf.WriteString(errout)
					buf.WriteString("\nf, err := os.Open(fn)")
					buf.WriteString(errout)
					buf.WriteString("\ndefer f.Close()")
					buf.WriteString("\n_, err = io.Copy(fw, f)")
					buf.WriteString(errout)
					buf.WriteString("\n}")
				}
				buf.WriteString("\nerr = w.Close()")
				buf.WriteString(errout)
			} else {
				buf.WriteString("\n" + `err = json.NewEncoder(&buf).Encode(in)`)
				buf.WriteString(errout)
			}
		}
	}

	switch method {
	case "get":
		buf.WriteString("\nif pdebug.Enabled {")
		fmt.Fprintf(&buf, "\npdebug.Printf(%s, u.String())", strconv.Quote("GET to %s"))
		buf.WriteString("\n}")
		buf.WriteString("\n" + `req, err := http.NewRequest("GET", u.String(), nil)`)
		buf.WriteString(errout)
	case "post":
		buf.WriteString("\nif pdebug.Enabled {")
		fmt.Fprintf(&buf, "\npdebug.Printf(%s, u.String())", strconv.Quote("POST to %s"))
		buf.WriteString("\n" + `pdebug.Printf("%s", buf.String())`)
		buf.WriteString("\n}")
		buf.WriteString("\n" + `req, err := http.NewRequest("POST", u.String(), &buf)`)
		buf.WriteString(errout)

		if l.EncType == "multipart/form-data" {
			// Must create a multipart/form-data request
			buf.WriteString("\nreq.Header.Set(\"Content-Type\", w.FormDataContentType())")
		} else {
			buf.WriteString("\n" + `req.Header.Set("Content-Type", "application/json")`)
		}
	}

	buf.WriteString("\n" + `if c.basicAuth.username != "" && c.basicAuth.password != "" {`)
	buf.WriteString("\nreq.SetBasicAuth(c.basicAuth.username, c.basicAuth.password)")
	buf.WriteString("\n}")
	buf.WriteString("\n\nif m := c.mutator; m != nil {")
	buf.WriteString("\nif err := m(req); err != nil {")
	buf.WriteString("\nreturn ")
	if outtype != "" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("errors.Wrap(err, `failed to mutate request`)")
	buf.WriteString("\n}")
	buf.WriteString("\n}")
	buf.WriteString("\n" + `res, err := c.client.Do(req)`)
	buf.WriteString(errout)

	buf.WriteString("\nif res.StatusCode != http.StatusOK {")
	// If in case of an error, we should at least attempt to parse the
	// resulting JSON
	buf.WriteString("\nif strings.HasPrefix(strings.ToLower(res.Header.Get(`Content-Type`)), `application/json`) {")
	buf.WriteString("\nvar errjson ErrJSON")
	buf.WriteString("\nif err := json.NewDecoder(res.Body).Decode(&errjson); err != nil {")
	buf.WriteString("\nreturn ")
	if outtype != "" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("errors.Errorf(`Invalid response: '%s'`, res.Status)")
	buf.WriteString("\n}")

	buf.WriteString("\nif len(errjson.Error) > 0 {")
	buf.WriteString("\nreturn ")
	if outtype != "" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("errors.New(errjson.Error)")
	buf.WriteString("\n}")
	buf.WriteString("\n}")
	buf.WriteString("\nreturn ")
	if outtype != "" {
		buf.WriteString("nil, ")
	}
	buf.WriteString("errors.Errorf(`Invalid response: '%s'`, res.Status)")
	buf.WriteString("\n}")
	if outtype == "" {
		buf.WriteString("\nreturn nil")
	} else {

		buf.WriteString("\njsonbuf := getTransportJSONBuffer()")
		buf.WriteString("\ndefer releaseTransportJSONBuffer(jsonbuf)")
		buf.WriteString("\n_, err = io.Copy(jsonbuf, io.LimitReader(res.Body, MaxResponseSize))")
		buf.WriteString("\ndefer res.Body.Close()")
		buf.WriteString("\nif pdebug.Enabled {")
		buf.WriteString("\nif err != nil {")
		buf.WriteString("\n" + `pdebug.Printf("failed to read respons buffer: %s", err)`)
		buf.WriteString("\n} else {")
		buf.WriteString("\n" + `pdebug.Printf("response buffer: %s", jsonbuf)`)
		buf.WriteString("\n}")
		buf.WriteString("\n}")
		buf.WriteString(errout)
		buf.WriteString("\n\nvar payload ")
		buf.WriteString(outtype)
		buf.WriteString("\nerr = json.Unmarshal(jsonbuf.Bytes(), &payload)")
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

	imports := []string{"github.com/lestrrat/go-pdebug", "github.com/lestrrat/go-urlenc", "github.com/pkg/errors"}
	if l := ctx.ClientHints.Imports; len(l) > 0 {
		imports = append(imports, l...)
	}

	genutil.WriteImports(
		&buf,
		[]string{"bytes", "encoding/json", "io", "mime/multipart", "net/http", "net/url", "os", "strings", "sync"},
		imports,
	)

	buf.WriteString(`
const MaxResponseSize = (1<<20)*2
var _ = bytes.MinRead
var _ = json.Decoder{}
var _ = multipart.Form{}
var _ = os.Stdout
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

type BasicAuth struct {
	username string
	password string
}

func (a BasicAuth) Username() string {
	return a.username
}

func (a BasicAuth) Password() string {
	return a.password
}

type ErrJSON struct {
	Error string ` + "`" + `json:"error,omitempty"` + "`" + `
}

type Client struct {
	basicAuth BasicAuth
	client *http.Client
	endpoint string
	mutator  func(*http.Request) error
}

func New(s string) *Client {
	return &Client{
		client: &http.Client{},
		endpoint: s,
	}
}

func (c *Client) BasicAuth() BasicAuth {
	return c.basicAuth
}

func (c *Client) SetAuth(username, password string) {
	c.basicAuth.username = username
	c.basicAuth.password = password
}

func (c *Client) Client() *http.Client {
	return c.client
}

func (c *Client) Endpoint() string {
	return c.endpoint
}

func (c *Client) SetMutator(m func(*http.Request) error) {
	c.mutator = m
}

`)

	// for each endpoint, create a method that accepts
	for _, methodName := range ctx.MethodNames {
		method := ctx.Methods[methodName]
		fmt.Fprint(&buf, method)
		fmt.Fprint(&buf, "\n\n")
	}

	if err := genutil.WriteFmtCode(out, &buf); err != nil {
		return err
	}

	return nil
}
