package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/lsp"
)

func TestLSPSecurityControlErrorsHaveStableFailClosedMappings(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "rate", err: lsp.ErrRateLimited, status: http.StatusTooManyRequests, code: "lsp_rate_limited"},
		{name: "audit", err: lsp.ErrAuditUnavailable, status: http.StatusServiceUnavailable, code: "lsp_ticket_store_unavailable"},
	} {
		t.Run(test.name+" websocket", func(t *testing.T) {
			status, code, _ := lspConsumeProblem(test.err)
			if status != test.status || code != test.code {
				t.Fatalf("consume mapping = %d %q", status, code)
			}
		})
		t.Run(test.name+" HTTP", func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/sandbox-sessions/example/lsp-tickets", nil)
			writeLSPProblem(context, test.err)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("HTTP mapping = %d %s", response.Code, response.Body.String())
			}
		})
	}
}
