package genutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lestrrat/go-jsschema"
	"github.com/lestrrat/go-jsval"
	"github.com/lestrrat/go-jsval/builder"
)

var rxif = regexp.MustCompile(`\s*interface\s*{\s*}\s*`)

func LooksLikeStruct(s string) bool {
	if rxif.MatchString(s) {
		return false
	}
	return !strings.HasPrefix(s, "[]") && !strings.HasPrefix(s, "map[")
}

var wsrx = regexp.MustCompile(`\s+`)

func TitleToName(s string) string {
	buf := bytes.Buffer{}
	for _, p := range wsrx.Split(s, -1) {
		buf.WriteString(strings.ToUpper(p[:1]))
		buf.WriteString(p[1:])
	}
	return buf.String()
}

func MakeValidator(s *schema.Schema, ctx interface{}) (*jsval.JSVal, error) {
	b := builder.New()
	v, err := b.BuildWithCtx(s, ctx)
	if err != nil {
		return nil, err
	}

	return v, nil
}

func WriteImports(out io.Writer, stdlibs, extlibs []string) error {
	if len(stdlibs) == 0 && len(extlibs) == 0 {
		return nil
	}

	fmt.Fprint(out, "import (\n")
	for _, pname := range stdlibs {
		fmt.Fprint(out, "\t"+`"`+pname+`"`+"\n")
	}
	if len(extlibs) > 0 {
		if len(stdlibs) > 0 {
			fmt.Fprint(out, "\n")
		}
		for _, pname := range extlibs {
			fmt.Fprint(out, "\t"+`"`+pname+`"`+"\n")
		}
	}
	fmt.Fprint(out, ")\n\n")
	return nil
}

func CreateFile(fn string) (*os.File, error) {
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
