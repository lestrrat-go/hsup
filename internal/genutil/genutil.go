package genutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

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
