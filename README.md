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

