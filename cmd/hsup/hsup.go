package main

import (
	"log"
	"os"

	"github.com/lestrrat/go-hsup"
)

func main() {
	os.Exit(_main())
}

func _main() int {
	b := hsup.NetHTTP
	if err := b.ProcessFile(os.Args[1]); err != nil {
		log.Printf("%s", err)
		return 1
	}
	return 0
}