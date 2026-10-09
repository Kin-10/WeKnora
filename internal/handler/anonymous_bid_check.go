package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/bidcheck"
	"github.com/Tencent/WeKnora/internal/bidformat"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

const anonymousBidFileLimit = bidcheck.MaxFileBytes
const anonymousBidTextLimit = 4 << 20
const anonymousBidRequestLimit = 2*anonymousBidFileLimit + (1 << 20)

// AnonymousBidCheckHandler checks uploaded documents without creating a chat,
// importing knowledge, or retaining the source files in workspace storage.
type AnonymousBidCheckHandler struct {
	documentReader interfaces.DocumentReader
	tenantService  interfaces.TenantService
	slots          chan struct{}
}

func NewAnonymousBidCheckHandler(reader interfaces.DocumentReader, tenants interfaces.TenantService) *AnonymousBidCheckHandler {
	return &AnonymousBidCheckHandler{documentReader: reader, tenantService: tenants, slots: make(chan struct{}, 2)}
}

func (h *AnonymousBidCheckHandler) Check(c *gin.Context) {
	// Bound multipart reads as well as analysis: slow uploads must not retain
	// every processing slot indefinitely on a server without a global timeout.
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(anonymousBidUploadTimeout(c.Request.ContentLength)))
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": "当前检查任务较多，请稍后重试。"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, anonymousBidRequestLimit)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		c.Error(apperrors.NewBadRequestError("请上传招标文件和标书，每个文件不超过 200 MB。"))
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	defer c.Request.MultipartForm.RemoveAll()
	if len(c.Request.MultipartForm.File["tender_file"]) != 1 || len(c.Request.MultipartForm.File["bid_file"]) != 1 {
		c.Error(apperrors.NewBadRequestError("请分别上传一份招标文件和一份待检查标书。"))
		return
	}
	scope := bidformat.Scope(strings.TrimSpace(c.PostForm("scope")))
	if scope == "" {
		scope = bidformat.ScopeTechnical
	}
	if scope != bidformat.ScopeTechnical && scope != bidformat.ScopeBusiness && scope != bidformat.ScopeAll {
		c.Error(apperrors.NewBadRequestError("请选择有效的检查范围。"))
		return
	}
	keywords, err := anonymousBidKeywords(c.PostForm("identity_keywords"))
	if err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	tender, err := readAnonymousBidUpload(c, "tender_file")
	if err != nil {
		c.Error(apperrors.NewBadRequestError("招标文件：" + err.Error()))
		return
	}
	bid, err := readAnonymousBidUpload(c, "bid_file")
	if err != nil {
		c.Error(apperrors.NewBadRequestError("待检查标书：" + err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Second)
	defer cancel()
	for _, document := range []*bidcheck.Document{&tender, &bid} {
		if err := h.extract(ctx, document); err != nil {
			logger.Warnf(ctx, "Anonymous bid check could not extract %s document: %v", document.Format, err)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "message": fmt.Sprintf("无法完整读取文件「%s」。请使用可读取的 Word/PDF；扫描件需先完成 OCR 后重新上传。", document.Name)})
			return
		}
	}
	report, err := bidcheck.Analyze(bidcheck.Request{Tender: tender, Bid: bid, Scope: scope, IdentityKeywords: keywords})
	if err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	report.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": report})
}

func anonymousBidKeywords(raw string) ([]string, error) {
	if len(raw) > 16<<10 {
		return nil, fmt.Errorf("身份关键词过长。")
	}
	var items []string
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, fmt.Errorf("身份关键词格式无效。")
		}
	} else {
		items = strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' })
	}
	if len(items) > 50 {
		return nil, fmt.Errorf("最多填写 50 个身份关键词。")
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]bool)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if !utf8.ValidString(item) || utf8.RuneCountInString(item) < 2 || utf8.RuneCountInString(item) > 100 {
			return nil, fmt.Errorf("每个身份关键词应为 2 至 100 个字符。")
		}
		seen[item] = true
		result = append(result, item)
	}
	return result, nil
}

func readAnonymousBidUpload(c *gin.Context, field string) (bidcheck.Document, error) {
	header, err := c.FormFile(field)
	if err != nil {
		return bidcheck.Document{}, fmt.Errorf("请选择文件。")
	}
	name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	switch format {
	case "docx", "doc", "pdf", "txt", "md":
	default:
		return bidcheck.Document{}, fmt.Errorf("支持 PDF、DOCX、DOC、TXT 和 Markdown 文件。")
	}
	if header.Size <= 0 || header.Size > anonymousBidFileLimit {
		return bidcheck.Document{}, fmt.Errorf("文件大小应在 0 至 200 MB 之间。")
	}
	file, err := header.Open()
	if err != nil {
		return bidcheck.Document{}, fmt.Errorf("无法打开文件。")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, anonymousBidFileLimit+1))
	if err != nil || len(data) == 0 || len(data) > anonymousBidFileLimit {
		return bidcheck.Document{}, fmt.Errorf("无法读取文件或文件超过 200 MB。")
	}
	valid := true
	switch format {
	case "pdf":
		valid = bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-"))
	case "docx":
		valid = bytes.HasPrefix(data, []byte("PK\x03\x04"))
	case "doc":
		valid = bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	case "txt", "md":
		valid = utf8.Valid(data) && !bytes.Contains(data, []byte{0})
	}
	if !valid {
		return bidcheck.Document{}, fmt.Errorf("文件内容与扩展名不符；文本文件请使用 UTF-8 编码。")
	}
	return bidcheck.Document{Name: name, Format: format, Data: data}, nil
}

// Match the frontend upload budget of 10 MiB/minute; analysis has a separate
// deadline after the body is received. Chunked uploads use the finite body cap.
func anonymousBidUploadTimeout(contentLength int64) time.Duration {
	if contentLength <= 0 || contentLength > anonymousBidRequestLimit {
		contentLength = anonymousBidRequestLimit
	}
	// Scale in seconds first so a 400 MiB request does not overflow a
	// nanosecond-based time.Duration during multiplication.
	seconds := (contentLength*60 + (10 << 20) - 1) / (10 << 20)
	budget := time.Duration(seconds) * time.Second
	return max(2*time.Minute, budget+time.Minute)
}

func (h *AnonymousBidCheckHandler) extract(ctx context.Context, document *bidcheck.Document) error {
	switch document.Format {
	case "txt", "md":
		document.Text = string(document.Data)
	case "docx":
		text, err := bidcheck.ExtractDOCXText(document.Data)
		if err != nil {
			return err
		}
		document.Text = text
	default:
		engine := ""
		var overrides map[string]string
		if tenant, ok := ctx.Value(types.TenantInfoContextKey).(*types.Tenant); ok && tenant != nil && tenant.ParserEngineConfig != nil {
			engine = tenant.ParserEngineConfig.ResolveChatParserEngine(document.Format)
			overrides = tenant.ParserEngineConfig.ToOverridesMap()
		}
		if engine == "auto" {
			engine = ""
		}
		deps := docparser.ReaderDeps{Remote: h.documentReader, Overrides: overrides}
		if h.tenantService != nil {
			deps.WeKnoraCloudCredentials = h.tenantService.GetWeKnoraCloudCredentials
		}
		reader, err := docparser.NewReader(ctx, engine, document.Format, false, deps)
		if err != nil {
			return err
		}
		result, err := reader.Read(ctx, &types.ReadRequest{FileContent: document.Data, FileName: document.Name, FileType: document.Format, ParserEngine: engine, ParserEngineOverrides: overrides})
		if err != nil {
			return err
		}
		if result == nil || result.Error != "" {
			return fmt.Errorf("document parser did not return complete text")
		}
		document.Text = anonymousBidPageMarkers(result)
		document.VerifiedPages = document.Text != result.MarkdownContent
	}
	if strings.TrimSpace(document.Text) == "" || len(document.Text) > anonymousBidTextLimit {
		return fmt.Errorf("document text is empty or exceeds the analysis limit")
	}
	return nil
}

// Only parser-provided physical PDF locators become page markers. In
// particular, printed page numbers and guessed page counts are not locators.
func anonymousBidPageMarkers(result *types.ReadResult) string {
	text := []rune(result.MarkdownContent)
	blocks := append([]types.SourceBlock(nil), result.SourceBlocks...)
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Start < blocks[j].Start })
	var output strings.Builder
	last, page := 0, 0
	for _, block := range blocks {
		if block.Locator.Type != types.SourceLocatorPDF || block.Locator.Page < 1 || block.Locator.Partial ||
			block.Start < last || block.Start < 0 || block.Start >= len(text) || block.End <= block.Start || block.End > len(text) ||
			block.Locator.Page == page {
			continue
		}
		output.WriteString(string(text[last:block.Start]))
		fmt.Fprintf(&output, "\n---PHYSICAL PAGE %d---\n", block.Locator.Page)
		last, page = block.Start, block.Locator.Page
	}
	if last == 0 && page == 0 {
		return result.MarkdownContent
	}
	output.WriteString(string(text[last:]))
	return output.String()
}
