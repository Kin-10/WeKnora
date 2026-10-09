package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func anonymousCheckRequest(t *testing.T, tender, bid, scope, keywords string) (*http.Request, *bytes.Buffer) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, file := range []struct{ field, content string }{{"tender_file", tender}, {"bid_file", bid}} {
		if file.content == "" {
			continue
		}
		part, err := w.CreateFormFile(file.field, file.field+".txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(file.content))
	}
	_ = w.WriteField("scope", scope)
	_ = w.WriteField("identity_keywords", keywords)
	_ = w.Close()
	request := httptest.NewRequest(http.MethodPost, "/check", &body)
	request.Header.Set("Content-Type", w.FormDataContentType())
	return request, &body
}

func TestAnonymousBidCheckUploadsAndReport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/check", NewAnonymousBidCheckHandler(nil, nil).Check)
	request, _ := anonymousCheckRequest(t, "技术标（暗标）制作要求：不得出现投标人名称。正文字体采用宋体四号。", "技术方案\n本项目由示例企业实施。", "technical", `["示例企业"]`)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Status    string `json:"status"`
			CheckedAt string `json:"checked_at"`
			Checks    []struct {
				Excerpt string `json:"excerpt"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Data.Status != "fail" || response.Data.CheckedAt == "" {
		t.Fatalf("expected identified violation with timestamp: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "示例企业") {
		t.Fatal("report omitted the bid evidence")
	}
}

func TestAnonymousBidCheckRejectsInvalidInputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ name, tender, bid, scope, keywords string }{
		{"missing bid", "招标要求", "", "technical", ""},
		{"invalid scope", "招标要求", "标书内容", "other", ""},
		{"invalid keywords", "招标要求", "标书内容", "technical", `[1]`},
		{"short keywords", "招标要求", "标书内容", "technical", `["甲"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(middleware.ErrorHandler())
			engine.POST("/check", NewAnonymousBidCheckHandler(nil, nil).Check)
			request, _ := anonymousCheckRequest(t, tc.tender, tc.bid, tc.scope, tc.keywords)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAnonymousBidCheckNoRequirementsIsNotPassed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/check", NewAnonymousBidCheckHandler(nil, nil).Check)
	request, _ := anonymousCheckRequest(t, "采购办公设备一批。", "项目实施计划。", "technical", "")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"review"`) {
		t.Fatalf("missing tender rules must require review: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAnonymousBidPhysicalPageMarkersKeepRuneOffsets(t *testing.T) {
	result := &types.ReadResult{MarkdownContent: "第一页中文。\n第二页正文。", SourceBlocks: []types.SourceBlock{
		{Start: 7, End: 13, Locator: types.SourceLocator{Type: types.SourceLocatorPDF, Page: 2}},
		{Start: 0, End: 6, Locator: types.SourceLocator{Type: types.SourceLocatorPDF, Page: 1}},
		{Start: 999, End: 1000, Locator: types.SourceLocator{Type: types.SourceLocatorPDF, Page: 9}},
	}}
	marked := anonymousBidPageMarkers(result)
	if !strings.Contains(marked, "PAGE 1---\n第一页中文。") || !strings.Contains(marked, "PAGE 2---\n第二页正文。") || strings.Contains(marked, "PAGE 9") {
		t.Fatalf("invalid page mapping: %q", marked)
	}
	if got := anonymousBidPageMarkers(&types.ReadResult{MarkdownContent: "未知页码。"}); got != "未知页码。" {
		t.Fatal("unmapped text should not acquire inferred pages")
	}
}

type anonymousCheckReader struct {
	interfaces.DocumentReader
	requests []*types.ReadRequest
}

func (r *anonymousCheckReader) Read(_ context.Context, request *types.ReadRequest) (*types.ReadResult, error) {
	r.requests = append(r.requests, request)
	text := "技术标（暗标）制作要求：不得出现投标人名称。正文字体为宋体四号。"
	if request.FileName == "bid.pdf" {
		text = "项目实施方案由示例企业完成。"
	}
	return &types.ReadResult{MarkdownContent: text, SourceBlocks: []types.SourceBlock{{Start: 0, End: len([]rune(text)), Locator: types.SourceLocator{Type: types.SourceLocatorPDF, Page: 3}}}}, nil
}

func TestAnonymousBidPDFUsesParserAndRetainsReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reader := &anonymousCheckReader{}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/check", NewAnonymousBidCheckHandler(reader, nil).Check)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, item := range []struct{ field, name string }{{"tender_file", "tender.pdf"}, {"bid_file", "bid.pdf"}} {
		part, err := w.CreateFormFile(item.field, item.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("%PDF-1.7\nparser stub fixture"))
	}
	_ = w.Close()
	request := httptest.NewRequest(http.MethodPost, "/check", &body)
	request.Header.Set("Content-Type", w.FormDataContentType())
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(reader.requests) != 2 {
		t.Fatalf("parser integration failed: status=%d calls=%d body=%s", recorder.Code, len(reader.requests), recorder.Body.String())
	}
	var response struct {
		Data struct {
			Status  string `json:"status"`
			Summary struct {
				Review int `json:"review"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Status == "pass" || response.Data.Summary.Review == 0 || !strings.Contains(recorder.Body.String(), `"source_page":3`) {
		t.Fatalf("PDF formatting/coverage must remain review and retain verified pages: %s", recorder.Body.String())
	}
	for _, request := range reader.requests {
		if request.URL != "" || request.FileType != "pdf" || len(request.FileContent) == 0 {
			t.Fatal("parser must receive uploaded bytes, not remote URLs")
		}
	}
}
