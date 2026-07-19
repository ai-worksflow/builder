package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

const (
	ServerControlDropNotification ServerControlDisposition = "drop-notification"
	ServerControlRespondContinue  ServerControlDisposition = "respond-continue"
	ServerControlRespondTerminate ServerControlDisposition = "respond-terminate"
	ServerControlTerminate        ServerControlDisposition = "terminate"

	serverControlUnknownRequestAuditMethod      = "unknown/serverRequest"
	serverControlUnknownNotificationAuditMethod = "unknown/serverNotification"
	serverControlInitializeAuditMethod          = "initialize"
	serverControlInvalidMessageAuditMethod      = "invalid/serverMessage"
)

var (
	ErrServerControlNotApplicable = errors.New("LSP server message is not a server control message")
	ErrServerControlMalformed     = errors.New("malformed LSP server control message")
	serverControlMethodPattern    = regexp.MustCompile(`^[A-Za-z0-9_$.-]+(?:/[A-Za-z0-9_$.-]+)*$`)
)

type ServerControlDisposition string

// ServerControlMessage is the payload-free projection for server callbacks
// that must never cross the browser boundary. Response is generated locally
// and contains only a normalized JSON-RPC ID plus a fixed MethodNotFound
// error; source params and server text are never retained.
type ServerControlMessage struct {
	Method      string
	Disposition ServerControlDisposition
	Response    []byte
	AuditCode   string
}

var ignoredServerNotifications = map[string]struct{}{
	"$/logTrace":         {},
	"$/progress":         {},
	"telemetry/event":    {},
	"window/logMessage":  {},
	"window/showMessage": {},
}

var recoverableServerRequests = map[string]struct{}{
	"client/registerCapability":      {},
	"client/unregisterCapability":    {},
	"window/showMessageRequest":      {},
	"window/workDoneProgress/create": {},
	"workspace/configuration":        {},
	"workspace/workspaceFolders":     {},
}

// retainedHighRiskServerRequests is deliberately finite. An unrecognized
// method is normalized before it reaches an audit DTO so a malicious language
// server cannot use the method string as a durable covert channel.
var retainedHighRiskServerRequests = map[string]struct{}{
	"window/showDocument":      {},
	"workspace/applyEdit":      {},
	"workspace/executeCommand": {},
}

// DecodeServerControlMessage recognizes only server-to-client requests and
// notifications that require an internal compatibility/security decision.
// Ordinary request responses and publishDiagnostics return NotApplicable and
// continue through the method-specific result filter.
func DecodeServerControlMessage(frame []byte, limits EffectiveLimits) (ServerControlMessage, error) {
	if len(frame) == 0 || limits.MaxFrameBytes <= 0 {
		return ServerControlMessage{}, ErrServerControlMalformed
	}
	if int64(len(frame)) > limits.MaxFrameBytes {
		return ServerControlMessage{}, errors.Join(
			ErrServerControlMalformed, ErrServerMessageTooLarge,
		)
	}
	if err := validateServerJSONDocument(frame, maxServerMessageDepth); err != nil {
		return ServerControlMessage{}, fmt.Errorf("%w: %v", ErrServerControlMalformed, err)
	}
	fields, err := decodeServerTopObject(frame)
	if err != nil {
		return ServerControlMessage{}, fmt.Errorf("%w: %v", ErrServerControlMalformed, err)
	}
	_, hasID := fields["id"]
	_, hasMethod := fields["method"]
	if !hasMethod {
		return ServerControlMessage{}, ErrServerControlNotApplicable
	}
	if err := requireJSONRPCVersion(fields["jsonrpc"]); err != nil {
		return ServerControlMessage{}, errors.Join(ErrServerControlMalformed, err)
	}
	method, err := decodeRequiredServerString(fields["method"], 256)
	if err != nil || !serverControlMethodPattern.MatchString(method) {
		return ServerControlMessage{}, errors.Join(ErrServerControlMalformed, err)
	}

	if !hasID {
		if method == "textDocument/publishDiagnostics" {
			return ServerControlMessage{}, ErrServerControlNotApplicable
		}
		if err := requireServerFields(fields, []string{"jsonrpc", "method"}, []string{"params"}); err != nil {
			return ServerControlMessage{}, fmt.Errorf("%w: %v", ErrServerControlMalformed, err)
		}
		if _, ignored := ignoredServerNotifications[method]; ignored {
			return ServerControlMessage{
				Method: method, Disposition: ServerControlDropNotification,
			}, nil
		}
		return ServerControlMessage{
			Method: serverControlAuditMethod(method, false), Disposition: ServerControlTerminate,
			AuditCode: "server_notification_forbidden",
		}, nil
	}

	if err := requireServerFields(fields, []string{"jsonrpc", "id", "method"}, []string{"params"}); err != nil {
		return ServerControlMessage{}, fmt.Errorf("%w: %v", ErrServerControlMalformed, err)
	}
	id, err := normalizeServerControlID(fields["id"])
	if err != nil {
		return ServerControlMessage{}, errors.Join(ErrServerControlMalformed, err)
	}
	response, err := marshalServerMethodNotFound(id, limits.MaxFrameBytes)
	if err != nil {
		return ServerControlMessage{}, err
	}
	if _, recoverable := recoverableServerRequests[method]; recoverable {
		return ServerControlMessage{
			Method: method, Disposition: ServerControlRespondContinue,
			Response: response, AuditCode: "server_request_rejected",
		}, nil
	}
	return ServerControlMessage{
		Method: serverControlAuditMethod(method, true), Disposition: ServerControlRespondTerminate,
		Response: response, AuditCode: "server_request_forbidden",
	}, nil
}

func serverControlAuditMethod(method string, request bool) string {
	if _, retained := retainedHighRiskServerRequests[method]; retained {
		return method
	}
	if request {
		return serverControlUnknownRequestAuditMethod
	}
	return serverControlUnknownNotificationAuditMethod
}

func auditableServerControlMethod(method string) bool {
	if method == serverControlUnknownRequestAuditMethod ||
		method == serverControlUnknownNotificationAuditMethod ||
		method == serverControlInitializeAuditMethod ||
		method == serverControlInvalidMessageAuditMethod {
		return true
	}
	if _, recoverable := recoverableServerRequests[method]; recoverable {
		return true
	}
	_, retained := retainedHighRiskServerRequests[method]
	return retained
}

func normalizeServerControlID(value json.RawMessage) (json.RawMessage, error) {
	normalized := bytes.TrimSpace(value)
	if len(normalized) == 0 || bytes.Equal(normalized, []byte("null")) {
		return nil, ErrServerControlMalformed
	}
	if normalized[0] == '"' {
		decoded, err := decodeRequiredServerString(normalized, 64)
		if err != nil {
			return nil, ErrServerControlMalformed
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			return nil, ErrServerControlMalformed
		}
		return encoded, nil
	}
	if !validSafeIntegerLexeme(string(normalized)) {
		return nil, ErrServerControlMalformed
	}
	return slices.Clone(normalized), nil
}

func marshalServerMethodNotFound(id json.RawMessage, maximum int64) ([]byte, error) {
	response, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      slices.Clone(id),
		Error: struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		}{Code: -32601, Message: "Method not found"},
	})
	if err != nil || int64(len(response)) > maximum {
		return nil, ErrServerControlMalformed
	}
	return response, nil
}
