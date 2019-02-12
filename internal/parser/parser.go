package parser

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lestrrat-go/hsup/ext"
	"github.com/lestrrat-go/hsup/internal/genutil"
	"github.com/lestrrat-go/jshschema"
	"github.com/lestrrat-go/jsval"
	"github.com/pkg/errors"
)

type Result struct {
	Schema              *hschema.HyperSchema
	Methods             map[string]string
	MethodNames         []string
	MethodWrappers      map[string][]string
	Middlewares         []string
	PathToMethods       map[string]string
	RequestCORS         map[string]string
	RequestMutators     map[string][]string
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
		RequestCORS:         make(map[string]string),
		RequestMutators:     make(map[string][]string),
		RequestPayloadType:  make(map[string]string),
		RequestValidators:   make(map[string]*jsval.JSVal),
		ResponseValidators:  make(map[string]*jsval.JSVal),
		ResponsePayloadType: make(map[string]string),
	}

	if err := parse(&ctx, s); err != nil {
		return nil, errors.Wrap(err, "failed to parse JSON hyper schema")
	}
	return &ctx, nil
}

func parse(ctx *Result, s *hschema.HyperSchema) error {
	middlewares, ok := s.Extras[ext.MiddlewareKey]
	if ok {
		var mwlist []interface{}
		mwlist, ok = middlewares.([]interface{})
		if ok {
			ctx.Middlewares = make([]string, len(mwlist))
			for i, mw := range mwlist {
				ctx.Middlewares[i] = mw.(string)
			}
		}
	}

	// We want to know the namespace of the transport.
	// Normally we just use "model"
	transportNs, ok := s.Extras[ext.TransportNsKey]
	if !ok {
		transportNs = "model"
	}
	for i, link := range s.Links {
		if len(link.Title) == 0 {
			return errors.New("link " + strconv.Itoa(i) + ": hsup requires a 'title' element to generate resources")
		}

		methodName := genutil.TitleToName(link.Title)

		if v, ok := link.Extras[ext.CORSKey]; ok {
			ctx.RequestCORS[methodName] = v.(string)
		}

		if cmr, ok := link.Extras[ext.ClientMutateRequestKey]; ok {
			switch cmr.(type) {
			case string:
				ctx.RequestMutators[methodName] = []string{cmr.(string)}
			case []interface{}:
				list, ok := cmr.([]interface{})
				if !ok {
					return errors.Errorf(`%s must be a string or a list of strings`, ext.ClientMutateRequestKey)
				}
				cmrs := make([]string, len(list))
				for i, e := range list {
					s, ok := e.(string)
					if !ok {
						return errors.Errorf(`%s must be a string or a list of strings`, ext.ClientMutateRequestKey)
					}
					cmrs[i] = s
				}
				ctx.RequestMutators[methodName] = cmrs
			default:
				return errors.Errorf(`%s must be a string or a list of strings`, ext.ClientMutateRequestKey)
			}
		}
		// Got to do this first, because validators are used in makeMethod()
		if ls := link.Schema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.Schema)
				if err != nil {
					return errors.Wrap(err, "failed to resolve schema (request)")
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.Schema)
			if err != nil {
				return errors.Wrap(err, "failed to create request validator")
			}

			if gt, ok := ls.Extras[ext.TypeKey]; ok {
				ctx.RequestPayloadType[methodName] = gt.(string)
			} else {
				ctx.RequestPayloadType[methodName] = fmt.Sprintf("%s.%sRequest", transportNs, methodName)
			}
			v.Name = fmt.Sprintf("HTTP%sRequest", methodName)
			ctx.RequestValidators[methodName] = v

		}

		if ls := link.TargetSchema; ls != nil {
			if !ls.IsResolved() {
				rs, err := ls.Resolve(ctx.Schema)
				if err != nil {
					return errors.Wrap(err, "failed to resolve target schema (response)")
				}
				ls = rs
			}
			v, err := genutil.MakeValidator(ls, ctx.Schema)
			if err != nil {
				return errors.Wrap(err, "failed to create response validator")
			}
			ctx.ResponsePayloadType[methodName] = "interface{}"
			if gt, ok := ls.Extras[ext.TypeKey]; ok {
				ctx.ResponsePayloadType[methodName] = gt.(string)
			} else {
				ctx.ResponsePayloadType[methodName] = fmt.Sprintf("%s.%sResponse", transportNs, methodName)
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
