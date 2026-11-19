package webapi

import (
	"context"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// SystemHandler implements webv1connect.SystemServiceHandler (authenticated, no tenant data).
type SystemHandler struct{ version string }

// NewSystemHandler returns a SystemHandler reporting version.
func NewSystemHandler(version string) *SystemHandler { return &SystemHandler{version: version} }

// GetVersion returns the server version.
func (h *SystemHandler) GetVersion(context.Context, *connect.Request[webv1.GetVersionRequest]) (*connect.Response[webv1.GetVersionResponse], error) {
	return connect.NewResponse(&webv1.GetVersionResponse{Version: h.version}), nil
}
