package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-openapi/strfmt"
	apimodels "github.com/grafana/alerting-api/pkg/api"
	"github.com/grafana/grafana/pkg/api/response"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/datasourceproxy"
	"github.com/grafana/grafana/pkg/services/datasources"
	"gopkg.in/macaron.v1"
	"gopkg.in/yaml.v3"
)

var searchRegex = regexp.MustCompile(`\{(\w+)\}`)

func toMacaronPath(path string) string {
	return string(searchRegex.ReplaceAllFunc([]byte(path), func(s []byte) []byte {
		m := string(s[1 : len(s)-1])
		return []byte(fmt.Sprintf(":%s", m))
	}))
}

func timePtr(t strfmt.DateTime) *strfmt.DateTime {
	return &t
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func backendType(ctx *models.ReqContext, cache datasources.CacheService) (apimodels.Backend, error) {
	recipient := ctx.Params("Recipient")
	if recipient == apimodels.GrafanaBackend.String() {
		return apimodels.GrafanaBackend, nil
	}
	if datasourceID, err := strconv.ParseInt(recipient, 10, 64); err == nil {
		if ds, err := cache.GetDatasource(datasourceID, ctx.SignedInUser, ctx.SkipCache); err == nil {
			switch ds.Type {
			case "loki", "prometheus":
				return apimodels.LoTexRulerBackend, nil
			case "grafana-alertmanager-datasource":
				return apimodels.AlertmanagerBackend, nil
			default:
				return 0, fmt.Errorf("unexpected backend type (%v)", ds.Type)
			}
		}
	}
	return 0, fmt.Errorf("unexpected backend type (%v)", recipient)
}

// macaron unsafely asserts the http.ResponseWriter is an http.CloseNotifier, which will panic.
// Here we impl it, which will ensure this no longer happens, but neither will we take
// advantage cancelling upstream requests when the downstream has closed.
// NB: http.CloseNotifier is a deprecated ifc from before the context pkg.
type safeMacaronWrapper struct {
	http.ResponseWriter
}

func (w *safeMacaronWrapper) CloseNotify() <-chan bool {
	return make(chan bool)
}

// replacedResponseWriter overwrites the underlying responsewriter used by a *models.ReqContext.
// It's ugly because it needs to replace a value behind a few nested pointers.
func replacedResponseWriter(ctx *models.ReqContext) (*models.ReqContext, *response.NormalResponse) {
	resp := response.CreateNormalResponse(make(http.Header), nil, 0)
	cpy := *ctx
	cpyMCtx := *cpy.Context
	cpyMCtx.Resp = macaron.NewResponseWriter(ctx.Req.Method, &safeMacaronWrapper{resp})
	cpy.Context = &cpyMCtx
	return &cpy, resp
}

type AlertingProxy struct {
	DataProxy *datasourceproxy.DatasourceProxyService
}

// withReq proxies a different request
func (p *AlertingProxy) withReq(
	ctx *models.ReqContext,
	req *http.Request,
	extractor func([]byte) (interface{}, error),
) response.Response {
	newCtx, resp := replacedResponseWriter(ctx)
	newCtx.Req.Request = req
	p.DataProxy.ProxyDatasourceRequestWithID(newCtx, ctx.ParamsInt64("Recipient"))

	status := resp.Status()
	if status >= 400 {
		return response.Error(status, string(resp.Body()), nil)
	}

	t, err := extractor(resp.Body())
	if err != nil {
		return response.Error(500, err.Error(), nil)
	}

	b, err := json.Marshal(t)
	if err != nil {
		return response.Error(500, err.Error(), nil)
	}

	return response.JSON(status, b)
}

func yamlExtractor(v interface{}) func([]byte) (interface{}, error) {
	return func(b []byte) (interface{}, error) {
		decoder := yaml.NewDecoder(bytes.NewReader(b))
		decoder.KnownFields(true)

		err := decoder.Decode(v)

		return v, err
	}
}

func jsonExtractor(v interface{}) func([]byte) (interface{}, error) {
	if v == nil {
		// json unmarshal expects a pointer
		v = &map[string]interface{}{}
	}
	return func(b []byte) (interface{}, error) {
		return v, json.Unmarshal(b, v)
	}
}

func messageExtractor(b []byte) (interface{}, error) {
	return map[string]string{"message": string(b)}, nil
}
