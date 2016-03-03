# go-hsup

Generate scaffold web app from JSON Hyper Schema files

# Synopsis

Generate net/http flavored server code

```shell
hsup -s /path/to/hyper-schema.json -f nethttp
```

Generate http.Client based client code

```shell
hsup -s /path/to/hyper-schema.json -f httpclient
```

Generate both the net/http based server and the http client

```shell
hsup -s /path/to/hyper-schema.json -f nethttp -f httpclient
```

# JSON Schema Additions

Keys starting with `hsup.` are custom properties for hsup.

| Key          | Type                   | Description |
|:-------------|:-----------------------|:------------|
| hsup.type    | string                 | When specified within a link schema or targetSchema, this type is used to Marshal/Unmarshal data |
| hsup.wrapper | string, arrray(string) | When specified within a link, the named function is used to wrap the HandleFunc. The signature for the wrapper must be `func(http.HandleFunc) http.HandleeFunc` |
