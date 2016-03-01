package flavor

import (
	"fmt"
	"io"

	"github.com/lestrrat/go-jshschema"
)

type Flavor struct {
	MakeContext   func() interface{}
	Parse         func(interface{}, *hschema.HyperSchema) error
	GenerateFiles func(interface{}) error
}

var NetHTTP = Flavor{
	MakeContext:   makeContextForNetHTTP,
	Parse:         parseForNetHTTP,
	GenerateFiles: generateForNetHTTP,
}

func writeimports(out io.Writer, stdlibs, extlibs []string) error {
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
