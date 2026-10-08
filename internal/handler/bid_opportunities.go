package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

const (
	publicBidSearchURL          = "https://api-ack.lianqiai.cn/search/public/bid"
	publicBidSearchPageSize     = 20
	publicBidSearchMaxPage      = 100 // Product bound; not an upstream contract.
	publicBidSearchRequestLimit = 32 << 10
	publicBidSearchResultLimit  = 4 << 20
	publicBidDateLayout         = "2006-01-02 15:04:05"
)

// BidOpportunitiesHandler proxies only the documented public list endpoint.
// Upstream credentials, URLs, and undocumented filters cannot come from clients.
type BidOpportunitiesHandler struct {
	client *http.Client
}

func NewBidOpportunitiesHandler() *BidOpportunitiesHandler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 25 * time.Second
	return &BidOpportunitiesHandler{client: &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type publicBidSearchRequest struct {
	Keyword      string   `json:"keyword"`
	PageNum      int      `json:"pageNum"`
	PageSize     int      `json:"pageSize"`
	MatchType    string   `json:"matchType"`
	BidTypes     []string `json:"bidTypes"`
	Provinces    []string `json:"provinces"`
	Cities       []string `json:"cities"`
	Counties     []string `json:"counties"`
	StartTime    string   `json:"startTime"`
	EndTime      string   `json:"endTime"`
	TimeInterval int      `json:"timeInterval"`
	OrderBy      string   `json:"orderBy"`
	Time         string   `json:"time"`
}

type publicBidSearchUpstreamResponse struct {
	IsSucceed *bool             `json:"isSucceed"`
	Code      *int              `json:"code"`
	Total     *int64            `json:"total"`
	TraceID   string            `json:"traceId"`
	Data      []json.RawMessage `json:"data"`
}

type publicBidSearchResult struct {
	Items        []json.RawMessage `json:"items"`
	Total        *int64            `json:"total"`
	PageNum      int               `json:"page_num"`
	PageSize     int               `json:"page_size"`
	NextTime     string            `json:"next_time"`
	TraceID      string            `json:"trace_id"`
	UpstreamCode int               `json:"upstream_code"`
}

func validPublicBidText(value string, maxRunes int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func normalizePublicBidRequest(req *publicBidSearchRequest) error {
	req.Keyword = strings.TrimSpace(req.Keyword)
	if !validPublicBidText(req.Keyword, 200) {
		return errors.New("关键词最多 200 字，且不能包含控制字符")
	}
	if req.PageNum < 1 || req.PageNum > publicBidSearchMaxPage {
		return errors.New("本产品支持查询第 1 至 100 页")
	}
	if req.PageSize != publicBidSearchPageSize {
		return errors.New("本产品每页固定返回 20 条")
	}
	if req.MatchType == "" {
		req.MatchType = "term"
	}
	if req.MatchType != "term" && req.MatchType != "fuzzy" {
		return errors.New("匹配模式仅支持 term 或 fuzzy")
	}
	if req.OrderBy != "" && req.OrderBy != "PUBLISH_DATE_DESC" && req.OrderBy != "RELEVANCE" {
		return errors.New("不支持的排序方式")
	}
	switch req.TimeInterval {
	case 0, 1, 2, 3, 4, 5, 8, 9:
	default:
		return errors.New("不支持的预设时间范围")
	}
	if req.TimeInterval != 0 && (req.StartTime != "" || req.EndTime != "") {
		return errors.New("自定义日期与预设时间范围请选择一种")
	}
	for _, value := range []string{req.StartTime, req.EndTime} {
		if value == "" {
			continue
		}
		parsed, err := time.Parse(publicBidDateLayout, value)
		if err != nil || parsed.Format(publicBidDateLayout) != value {
			return errors.New("日期须为 YYYY-MM-DD HH:mm:ss 格式")
		}
	}
	if req.StartTime != "" && req.EndTime != "" && req.StartTime > req.EndTime {
		return errors.New("起始日期不能晚于截止日期")
	}
	// The cursor is opaque: only its size and control characters are restricted.
	if !validPublicBidText(req.Time, 200) {
		return errors.New("无效的分页游标")
	}
	if req.PageNum == 1 {
		req.Time = ""
	}
	for _, values := range [][]string{req.BidTypes, req.Provinces, req.Cities, req.Counties} {
		if len(values) > 100 {
			return errors.New("每组筛选条件最多 100 项")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || !validPublicBidText(value, 80) {
				return errors.New("筛选项不能为空，且最多 80 字")
			}
		}
	}
	for _, category := range req.BidTypes {
		switch category {
		case "招标公告", "中标结果", "采购意向", "审批项目":
		default:
			return errors.New("不支持的标讯类别")
		}
	}
	return nil
}

// publicBidPayload deliberately reconstructs all defaults. In particular, no
// unknown client-supplied field can turn this list query into another operation.
func publicBidPayload(req publicBidSearchRequest) map[string]any {
	list := func(value []string) []string {
		if value == nil {
			return []string{}
		}
		return value
	}
	return map[string]any{
		"subscribeId": nil, "keyword": req.Keyword,
		"provinces": list(req.Provinces), "cities": list(req.Cities), "regions": []string{}, "counties": list(req.Counties),
		"wtbProvince": []string{}, "wtbCity": []string{}, "bidTypes": list(req.BidTypes), "subBidTypes": []string{},
		"fields": []string{}, "keywords": []string{}, "containKeywords": []string{}, "excludes": []string{},
		"ownerTypes": []string{}, "industries": []string{}, "aiScorePoint": "",
		"startTime": req.StartTime, "endTime": req.EndTime, "startPreTime": "", "endPreTime": "",
		"timeInterval": req.TimeInterval, "amountInterval": 0,
		"minAmount": nil, "maxAmount": nil, "minWinBidAmount": nil, "maxWinBidAmount": nil,
		"orderBy": req.OrderBy, "isSubscribe": 0, "matchType": req.MatchType,
		"pageNum": req.PageNum, "pageSize": req.PageSize, "time": req.Time,
		"exportStartTime": "", "exportEndTime": "",
	}
}

func safePublicBidTrace(value string) string {
	var result strings.Builder
	for _, r := range value {
		if result.Len() >= 200 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_.:", r) {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func publicBidUpstreamError(status int, message string, code *int, traceID string) *apperrors.AppError {
	errorCode := apperrors.ErrServiceUnavailable
	if status == http.StatusGatewayTimeout {
		errorCode = apperrors.ErrTimeout
	} else if status == http.StatusTooManyRequests {
		errorCode = apperrors.ErrTooManyRequests
	}
	return (&apperrors.AppError{Code: errorCode, HTTPCode: status, Message: message}).WithDetails(gin.H{
		"upstream_code": code, "trace_id": traceID,
	})
}

func publicBidRequestTimedOut(err error) bool {
	var timeout net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}

func (h *BidOpportunitiesHandler) Search(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicBidSearchRequestLimit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	req := &publicBidSearchRequest{PageNum: 1, PageSize: publicBidSearchPageSize, MatchType: "term"}
	if err := decoder.Decode(&req); err != nil || req == nil {
		_ = c.Error(apperrors.NewBadRequestError("无效的标讯搜索参数"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		_ = c.Error(apperrors.NewBadRequestError("搜索参数必须为一个 JSON 对象"))
		return
	}
	if err := normalizePublicBidRequest(req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	payload, _ := json.Marshal(publicBidPayload(*req))
	upstreamRequest, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, publicBidSearchURL, bytes.NewReader(payload))
	if err != nil {
		_ = c.Error(apperrors.NewInternalServerError("无法创建标讯搜索请求"))
		return
	}
	upstreamRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	upstreamRequest.Header.Set("Accept", "application/json")
	response, err := h.client.Do(upstreamRequest)
	if err != nil {
		status, message := http.StatusBadGateway, "标讯搜索服务暂时不可用，请稍后重试"
		if publicBidRequestTimedOut(err) {
			status, message = http.StatusGatewayTimeout, "标讯搜索超时，请稍后重试"
		}
		logger.Warnf(c.Request.Context(), "Public bid search transport failure: timeout=%t cancelled=%t", status == http.StatusGatewayTimeout, errors.Is(err, context.Canceled))
		_ = c.Error(publicBidUpstreamError(status, message, nil, ""))
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, publicBidSearchResultLimit+1))
	if err != nil || len(body) > publicBidSearchResultLimit {
		logger.Warnf(c.Request.Context(), "Public bid search unreadable response: http_status=%d oversized=%t", response.StatusCode, len(body) > publicBidSearchResultLimit)
		status, message := http.StatusBadGateway, "标讯服务返回的数据无法读取，请稍后重试"
		if publicBidRequestTimedOut(err) {
			status, message = http.StatusGatewayTimeout, "标讯搜索超时，请稍后重试"
		}
		_ = c.Error(publicBidUpstreamError(status, message, nil, ""))
		return
	}
	var result publicBidSearchUpstreamResponse
	decodeErr := json.Unmarshal(body, &result)
	traceID := safePublicBidTrace(result.TraceID)
	logger.Infof(c.Request.Context(), "Public bid search response: http_status=%d upstream_code=%v trace_id=%s", response.StatusCode, publicBidCodeForLog(result.Code), traceID)
	// A 2xx transport response and an explicit business success are both required.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := http.StatusBadGateway
		message := "标讯搜索服务暂时不可用，请稍后重试"
		if response.StatusCode == http.StatusTooManyRequests {
			status, message = http.StatusTooManyRequests, "标讯搜索请求过于频繁，请稍后重试"
		} else if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			message = "标讯公开接口要求服务商登录或授权，当前无法查询后续结果"
		}
		_ = c.Error(publicBidUpstreamError(status, message, result.Code, traceID))
		return
	}
	if decodeErr != nil || result.IsSucceed == nil || result.Code == nil {
		_ = c.Error(publicBidUpstreamError(http.StatusBadGateway, "标讯服务返回的数据格式无效", result.Code, traceID))
		return
	}
	if !*result.IsSucceed || *result.Code != http.StatusOK {
		message := "标讯公开搜索未成功，请稍后重试"
		if *result.Code == http.StatusUnauthorized || *result.Code == http.StatusForbidden {
			message = "标讯公开接口要求服务商登录或授权，当前无法查询后续结果"
		}
		_ = c.Error(publicBidUpstreamError(http.StatusBadGateway, message, result.Code, traceID))
		return
	}
	if result.Total != nil && *result.Total < 0 {
		_ = c.Error(publicBidUpstreamError(http.StatusBadGateway, "标讯服务返回的总数无效", result.Code, traceID))
		return
	}
	if result.Data == nil {
		result.Data = []json.RawMessage{}
	}
	nextTime := ""
	for i, item := range result.Data {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '{' {
			_ = c.Error(publicBidUpstreamError(http.StatusBadGateway, "标讯服务返回的列表格式无效", result.Code, traceID))
			return
		}
		if i == len(result.Data)-1 {
			var cursor struct {
				Time string `json:"time"`
			}
			if err := json.Unmarshal(item, &cursor); err != nil || !validPublicBidText(cursor.Time, 200) {
				_ = c.Error(publicBidUpstreamError(http.StatusBadGateway, "标讯服务返回的分页游标无效", result.Code, traceID))
				return
			}
			nextTime = cursor.Time
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": publicBidSearchResult{
		Items: result.Data, Total: result.Total, PageNum: req.PageNum, PageSize: req.PageSize,
		NextTime: nextTime, TraceID: traceID, UpstreamCode: *result.Code,
	}})
}

func publicBidCodeForLog(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}
