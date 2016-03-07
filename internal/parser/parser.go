package parser

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lestrrat/go-hsup/ext"
	"github.com/lestrrat/go-hsup/internal/genutil"
	"github.com/lestrrat/go-jshschema"
	"github.com/lestrrat/go-jsval"
)

type Result struct {
	Schema              *hschema.HyperSchema
	Methods             map[string]string
	MethodNames         []string
	MethodWrappers      map[string][]string
	PathToMethods       map[string]string
	RequestPayloadType  map[string]string
	RequestValidators   map[string]*jsval.JSVal
	ResponsePayloadType map[string]string
	ResponseValidators  map[string]*jsval.JSVal
}

func Parse(s *hschema.HyperSchema) (*Result, error) {
	ctx := Result{
		Schema:              s,
		MethodNames:         make([]string, len(s.Links)),
		Methods:             make(map[string]string),
		MethodWrappers:      make(map[string][]string),
		PathToMethods:       make(map[string]string),
		RequestPayloadType:  make(map[string]string),
		RequestValidators:   make(map[string]*jsval.JSVal),
		ResponseValidators:  make(map[string]*jsval.JSVal),
		ResponsePayloadType: make(map[string]string),
	}

	if err := parse(&ctx, s); err != nil {
		return nil, err
	}
	return &ctx, nil
}

func parse(ctx *Result, s *hschema.HyperSchema) error {
	for i, link := range s.Links {
		methodName := genutil.TitleToName(link.Title)
		// Got to do this first, because validators are used in makeMethod()
		if ls := link.Schema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.Schema)
				if err != nil {
					return err
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.Schema)
			if err != nil {
				return err
			}

			// If the request is a GET request or the encoding is
			// 'application/x-www-form-urlencoded', then the input parameter
			// will HAVE to be a map
			if strings.ToLower(link.Method) == "get" || link.EncType == "application/x-www-form-urlencoded" {
				ctx.RequestPayloadType[methodName] = "map[string]interface{}"
			} else {
				ctx.RequestPayloadType[methodName] = "interface{}"
			}

			if gt, ok := ls.Extras[ext.TypeKey]; ok {
				ctx.RequestPayloadType[methodName] = gt.(string)
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.RequestValidators[methodName] = v
		}
		if ls := link.TargetSchema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.Schema)
				if err != nil {
					return err
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.Schema)
			if err != nil {
				return err
			}
			ctx.ResponsePayloadType[methodName] = "interface{}"
			if gt, ok := ls.Extras[ext.TypeKey]; ok {
				ctx.ResponsePayloadType[methodName] = gt.(string)
			}
			v.Name = fmt.Sprintf("HTTP%sResponse", methodName)
			ctx.ResponseValidators[methodName] = v
		}

		ctx.MethodNames[i] = methodName
		path := link.Path()
		if strings.IndexRune(path, '{') > -1 {
			return errors.New("found '{' in the URL. hsup does not support URI templates")
		}
		ctx.PathToMethods[path] = methodName

	}
	sort.Strings(ctx.MethodNames)
	return nil
}
