package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type publicBidRoundTripper func(*http.Request) (*http.Response, error)

func (f publicBidRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func publicBidTestRouter(h *BidOpportunitiesHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.POST("/search", h.Search)
	return router
}

func publicBidTestRequest(h *BidOpportunitiesHandler, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	// These credentials are for this product and must not reach the public API.
	request.Header.Set("Authorization", "Bearer private-test-key")
	request.Header.Set("token", "private-upstream-test-key")
	publicBidTestRouter(h).ServeHTTP(recorder, request)
	return recorder
}

func publicBidTestHandler(status int, body string) *BidOpportunitiesHandler {
	h := NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	return h
}

func TestPublicBidSearchBuildsFixedPayloadAndPreservesResults(t *testing.T) {
	h := NewBidOpportunitiesHandler()
	var payload map[string]any
	h.client.Transport = publicBidRoundTripper(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, publicBidSearchURL, req.URL.String())
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json; charset=utf-8", req.Header.Get("Content-Type"))
		require.Empty(t, req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("token"))
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"isSucceed":true,"code":200,"total":null,"traceId":"trace-success","data":[{"id":"first","md5Id":"other-id","title":"<a>检验试剂</a>","province":"","region":null,"bidUnit":null,"budgetAmount":"9.9万元","tags":[],"time":"previous"},{"id":"second","bidUnit":{"name":"医院","units":[{"name":"院区一"},{"name":"院区二"}]},"hasTenderFile":null,"time":"cursor 2026-10-08"}]}`))}, nil
	})
	response := publicBidTestRequest(h, `{"keyword":" IVD试剂 ","pageNum":2,"time":"incoming-cursor","bidTypes":["招标公告"],"provinces":["河北省"],"cities":["邯郸市"],"counties":["丛台区"],"startTime":"2026-10-01 00:00:00","endTime":"2026-10-08 23:59:59","orderBy":"PUBLISH_DATE_DESC"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "IVD试剂", payload["keyword"])
	require.Equal(t, float64(2), payload["pageNum"])
	require.Equal(t, float64(20), payload["pageSize"])
	require.Equal(t, "incoming-cursor", payload["time"])
	require.Equal(t, "term", payload["matchType"])
	require.Equal(t, []any{"河北省"}, payload["provinces"])
	for _, key := range []string{"regions", "wtbProvince", "wtbCity", "subBidTypes", "fields", "keywords", "containKeywords", "excludes", "ownerTypes", "industries"} {
		require.Equal(t, []any{}, payload[key], key)
	}
	for _, key := range []string{"subscribeId", "minAmount", "maxAmount", "minWinBidAmount", "maxWinBidAmount"} {
		require.Contains(t, payload, key)
		require.Nil(t, payload[key], key)
	}
	for _, key := range []string{"aiScorePoint", "startPreTime", "endPreTime", "exportStartTime", "exportEndTime"} {
		require.Equal(t, "", payload[key], key)
	}
	require.Equal(t, float64(0), payload["amountInterval"])
	require.Equal(t, float64(0), payload["isSubscribe"])
	require.NotContains(t, payload, "ifAttachmentUrls")
	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	data := result["data"].(map[string]any)
	require.Nil(t, data["total"])
	require.Equal(t, "cursor 2026-10-08", data["next_time"])
	require.Equal(t, float64(2), data["page_num"])
	require.Equal(t, float64(20), data["page_size"])
	require.Equal(t, "trace-success", data["trace_id"])
	items := data["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	require.Equal(t, "<a>检验试剂</a>", first["title"])
	require.Equal(t, "other-id", first["md5Id"])
	require.Equal(t, "", first["province"])
	require.Nil(t, first["region"])
	require.Equal(t, []any{}, first["tags"])
	require.Len(t, items[1].(map[string]any)["bidUnit"].(map[string]any)["units"], 2)
}

func TestPublicBidSearchFirstPageResetsCursorAndHandlesEmptyPage(t *testing.T) {
	for _, upstreamData := range []string{`null`, `[]`} {
		t.Run(upstreamData, func(t *testing.T) {
			h := NewBidOpportunitiesHandler()
			h.client.Transport = publicBidRoundTripper(func(req *http.Request) (*http.Response, error) {
				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, float64(1), payload["pageNum"])
				require.Equal(t, "", payload["time"])
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"isSucceed":true,"code":200,"total":0,"data":` + upstreamData + `}`))}, nil
			})
			response := publicBidTestRequest(h, `{"time":"stale-cursor"}`)
			require.Equal(t, 200, response.Code)
			require.JSONEq(t, `{"success":true,"data":{"items":[],"total":0,"page_num":1,"page_size":20,"next_time":"","trace_id":"","upstream_code":200}}`, response.Body.String())
		})
	}
}

func TestPublicBidSearchRejectsInvalidRequestBeforeCallingUpstream(t *testing.T) {
	cases := []string{
		`null`, `[]`, `1`, `{`, `{} {}`, `{"keyword":1}`, `{"pageNum":1.5}`,
		`{"url":"http://localhost/admin"}`, `{"token":"secret"}`, `{"subBidTypes":["anything"]}`, `{"industries":["unknown"]}`,
		`{"keyword":"` + strings.Repeat("字", 201) + `"}`, `{"keyword":"a\u0001b"}`,
		`{"pageNum":0}`, `{"pageNum":-1}`, `{"pageNum":101}`, `{"pageSize":0}`, `{"pageSize":21}`,
		`{"matchType":"other"}`, `{"orderBy":"other"}`, `{"timeInterval":6}`,
		`{"startTime":"2026-10-08"}`, `{"endTime":"2026-02-30 00:00:00"}`,
		`{"startTime":"2026-10-09 00:00:00","endTime":"2026-10-08 00:00:00"}`,
		`{"timeInterval":1,"startTime":"2026-10-08 00:00:00"}`,
		`{"time":"` + strings.Repeat("x", 201) + `"}`, `{"time":"cursor\nline"}`,
		`{"bidTypes":["AI评分点"]}`, `{"provinces":[""]}`,
		`{"cities":["` + strings.Repeat("字", 81) + `"]}`,
		`{"counties":[` + strings.TrimSuffix(strings.Repeat(`"区",`, 101), ",") + `]}`,
		`{"keyword":"` + strings.Repeat("x", publicBidSearchRequestLimit) + `"}`,
	}
	for _, body := range cases {
		t.Run(body[:min(60, len(body))], func(t *testing.T) {
			h := NewBidOpportunitiesHandler()
			h.client.Transport = publicBidRoundTripper(func(_ *http.Request) (*http.Response, error) {
				t.Fatal("invalid request reached the upstream")
				return nil, nil
			})
			response := publicBidTestRequest(h, body)
			require.Equal(t, 400, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "secret")
		})
	}
}

func TestPublicBidSearchAllowsDocumentedFilterValues(t *testing.T) {
	for _, interval := range []int{0, 1, 2, 3, 4, 5, 8, 9} {
		req := publicBidSearchRequest{PageNum: 100, PageSize: 20, MatchType: "fuzzy", TimeInterval: interval, OrderBy: "RELEVANCE", BidTypes: []string{"招标公告", "中标结果", "采购意向", "审批项目"}}
		require.NoError(t, normalizePublicBidRequest(&req))
	}
}

func TestPublicBidSearchChecksHTTPAndBusinessSuccess(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
		code   string
	}{
		{"http failure despite success body", 503, `{"isSucceed":true,"code":200,"traceId":"http-trace","data":[]}`, 502, "200"},
		{"http rate limit", 429, `{"isSucceed":false,"code":429,"traceId":"limit-trace"}`, 429, "429"},
		{"http forbidden", 403, `{"isSucceed":false,"code":403}`, 502, "403"},
		{"http HTML error", 500, `<html>internal-upstream-secret</html>`, 502, "null"},
		{"business failure", 200, `{"isSucceed":false,"code":401,"msg":"secret-token","traceId":"business-trace"}`, 502, "401"},
		{"wrong business code", 200, `{"isSucceed":true,"code":201,"data":[]}`, 502, "201"},
		{"malformed JSON", 200, `{"isSucceed":true`, 502, "null"},
		{"missing success", 200, `{"code":200,"data":[]}`, 502, "200"},
		{"missing code", 200, `{"isSucceed":true,"data":[]}`, 502, "null"},
		{"bad total", 200, `{"isSucceed":true,"code":200,"total":-1,"data":[]}`, 502, "200"},
		{"non-object item", 200, `{"isSucceed":true,"code":200,"data":[null]}`, 502, "200"},
		{"invalid list", 200, `{"isSucceed":true,"code":200,"data":"oops"}`, 502, "200"},
		{"invalid cursor type", 200, `{"isSucceed":true,"code":200,"data":[{"time":1}]}`, 502, "200"},
		{"invalid cursor text", 200, `{"isSucceed":true,"code":200,"data":[{"time":"line\nline"}]}`, 502, "200"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := publicBidTestRequest(publicBidTestHandler(test.status, test.body), `{}`)
			require.Equal(t, test.want, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `"upstream_code":`+test.code)
			require.NotContains(t, response.Body.String(), "secret")
			require.NotContains(t, response.Body.String(), "<html>")
		})
	}
}

func TestPublicBidSearchUpstreamAuthorizationDoesNotExpireProductLogin(t *testing.T) {
	for _, upstreamCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, httpStatus := range []int{http.StatusOK, upstreamCode} {
			t.Run(fmt.Sprintf("http_%d_business_%d", httpStatus, upstreamCode), func(t *testing.T) {
				body := fmt.Sprintf(`{"isSucceed":false,"code":%d,"msg":"暂无登陆状态，请进行登录!","traceId":"provider-auth-trace","data":[]}`, upstreamCode)
				response := publicBidTestRequest(publicBidTestHandler(httpStatus, body), `{"keyword":"IVD试剂","pageNum":2,"time":"previous-page-cursor"}`)
				// A provider login requirement must not trigger this product's 401
				// refresh/sign-out interceptor. The failure is upstream HTTP 502.
				require.Equal(t, http.StatusBadGateway, response.Code)
				var result struct {
					Success bool `json:"success"`
					Error   struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
						Details struct {
							UpstreamCode int    `json:"upstream_code"`
							TraceID      string `json:"trace_id"`
						} `json:"details"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
				require.False(t, result.Success)
				require.Equal(t, 1008, result.Error.Code)
				require.Equal(t, "标讯公开接口要求服务商登录或授权，当前无法查询后续结果", result.Error.Message)
				require.Equal(t, upstreamCode, result.Error.Details.UpstreamCode)
				require.Equal(t, "provider-auth-trace", result.Error.Details.TraceID)
			})
		}
	}
}

func TestPublicBidSearchBoundsResponseAndRedactsTrace(t *testing.T) {
	body := `{"isSucceed":true,"code":200,"total":null,"traceId":"abc\n<script>trace</script>","data":[]}`
	response := publicBidTestRequest(publicBidTestHandler(200, body), `{}`)
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"trace_id":"abcscripttracescript"`)
	response = publicBidTestRequest(publicBidTestHandler(200, strings.Repeat("x", publicBidSearchResultLimit+1)), `{}`)
	require.Equal(t, 502, response.Code)
	require.Less(t, response.Body.Len(), 1000)
}

func TestPublicBidSearchTransportErrorIsGeneric(t *testing.T) {
	h := NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network error with private-upstream-secret")
	})
	response := publicBidTestRequest(h, `{}`)
	require.Equal(t, 502, response.Code)
	require.NotContains(t, response.Body.String(), "private-upstream-secret")
}

func publicBidRewriteTransport(t *testing.T, server *httptest.Server) http.RoundTripper {
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	return publicBidRoundTripper(func(req *http.Request) (*http.Response, error) {
		request := req.Clone(req.Context())
		request.URL.Scheme, request.URL.Host = endpoint.Scheme, endpoint.Host
		return transport.RoundTrip(request)
	})
}

func TestPublicBidSearchNeverFollowsRedirect(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "/private-detail")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	h := NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRewriteTransport(t, server)
	response := publicBidTestRequest(h, `{}`)
	require.Equal(t, 502, response.Code)
	require.Equal(t, int32(1), hits.Load())
}

func TestPublicBidSearchPropagatesTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	h := NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRewriteTransport(t, server)
	h.client.Timeout = 20 * time.Millisecond
	response := publicBidTestRequest(h, `{}`)
	require.Equal(t, 504, response.Code)

	var called atomic.Int32
	h = NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRoundTripper(func(req *http.Request) (*http.Response, error) {
		called.Add(1)
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	publicBidTestRouter(h).ServeHTTP(recorder, request)
	require.Equal(t, 502, recorder.Code)
	require.Equal(t, int32(1), called.Load())
	require.NotContains(t, recorder.Body.String(), "context canceled")
}

func TestPublicBidSearchResponseBodyTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-req.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	h := NewBidOpportunitiesHandler()
	h.client.Transport = publicBidRewriteTransport(t, server)
	h.client.Timeout = 20 * time.Millisecond
	response := publicBidTestRequest(h, `{}`)
	require.Equal(t, 504, response.Code)
}
