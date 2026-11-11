package httpx

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Error struct {
	Code    string
	Status  int
	Message string
	Fields  map[string]string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Cause.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

func Invalid(fields map[string]string) *Error {
	return &Error{Code: "validation.invalid", Status: http.StatusUnprocessableEntity, Message: "提交内容有误", Fields: fields}
}

func NotFound(code, message string) *Error {
	return NewError(code, http.StatusNotFound, message)
}

func Forbidden(code, message string) *Error {
	return NewError(code, http.StatusForbidden, message)
}

func Unauthorized(message string) *Error {
	return NewError("auth.required", http.StatusUnauthorized, message)
}

// ErrorOf extracts an *Error or wraps an unknown error as internal.
func ErrorOf(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: "internal.error", Status: http.StatusInternalServerError, Message: "服务暂时不可用", Cause: err}
}

type Envelope struct {
	Data any            `json:"data,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Data: data, Meta: map[string]any{"request_id": RequestID(c)}})
}

func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{Data: data, Meta: map[string]any{"request_id": RequestID(c)}})
}

func Fail(c *gin.Context, err error) {
	e := ErrorOf(err)
	payload := gin.H{
		"error": gin.H{
			"code":       e.Code,
			"message":    e.Message,
			"request_id": RequestID(c),
		},
	}
	if len(e.Fields) > 0 {
		payload["error"].(gin.H)["fields"] = e.Fields
	}
	c.AbortWithStatusJSON(e.Status, payload)
}

func RequestID(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type Page struct {
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Total   int64 `json:"total"`
	HasNext bool  `json:"has_next"`
}

func NewPage(page, perPage int, total int64) Page {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	return Page{Page: page, PerPage: perPage, Total: total, HasNext: int64(page*perPage) < total}
}

func (p Page) Offset() int { return (p.Page - 1) * p.PerPage }
