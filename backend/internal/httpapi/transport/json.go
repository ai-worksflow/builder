package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

var (
	errEmptyJSONBody       = errors.New("JSON request body is required")
	errMultipleJSONValues  = errors.New("multiple JSON values are not allowed")
	errUnsupportedJSONType = errors.New("Content-Type must be application/json")
)

func DecodeJSON(context *gin.Context, destination interface{}, maxBytes int64) error {
	contentType := context.GetHeader("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return errUnsupportedJSONType
	}
	context.Request.Body = http.MaxBytesReader(context.Writer, context.Request.Body, maxBytes)
	decoder := json.NewDecoder(context.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if errors.Is(err, io.EOF) {
			return errEmptyJSONBody
		}
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errMultipleJSONValues
		}
		return err
	}
	return nil
}

func WriteJSONError(context *gin.Context, err error) {
	var maxBytesError *http.MaxBytesError
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.Is(err, errUnsupportedJSONType):
		problem.Write(context, problem.New(http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", err.Error()))
	case errors.As(err, &maxBytesError):
		problem.Write(context, problem.New(http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large", "The JSON request body exceeds the configured limit."))
	case errors.Is(err, errEmptyJSONBody), errors.Is(err, errMultipleJSONValues):
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_json", "Invalid JSON request", err.Error()))
	case errors.As(err, &syntaxError):
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_json", "Invalid JSON request", fmt.Sprintf("Malformed JSON near byte %d.", syntaxError.Offset)))
	case errors.As(err, &typeError):
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_json_type", "Invalid JSON value", fmt.Sprintf("Field %s has an invalid value type.", typeError.Field)))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		problem.Write(context, problem.New(http.StatusBadRequest, "unknown_json_field", "Unknown JSON field", err.Error()))
	default:
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_json", "Invalid JSON request", "The request body is not valid JSON."))
	}
}
