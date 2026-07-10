package transport

import "github.com/gin-gonic/gin"

// Gin mode is process-global. Set it once during package initialization so
// parallel router tests never race by mutating the mode while another test is
// registering routes.
func init() {
	gin.SetMode(gin.TestMode)
}
