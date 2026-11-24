package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

// TwoFactorHandler implements sermon.web.v1.TwoFactorService (behind the session-only
// Authenticator) and sermon.web.v1.SecurityPolicyService (behind the full one).
type TwoFactorHandler struct {
	svc        *twofactor.Service
	identities IdentityReader
	limiter    *ratelimit.Limiter
	logger     *slog.Logger
}

var (
	_ webv1connect.TwoFactorServiceHandler      = (*TwoFactorHandler)(nil)
	_ webv1connect.SecurityPolicyServiceHandler = (*TwoFactorHandler)(nil)
)

var errTooManyCodes = errors.New("too many attempts, please wait a moment")

// errTOTPNeedsPassword: accounts without a password (from OIDC) add a passkey as second factor.
var errTOTPNeedsPassword = errors.New("accounts without a password use a passkey as second factor")

// NewTwoFactorHandler returns the handler; limiter bounds setup confirmations per user.
func NewTwoFactorHandler(svc *twofactor.Service, identities IdentityReader, limiter *ratelimit.Limiter, logger *slog.Logger) *TwoFactorHandler {
	return &TwoFactorHandler{svc: svc, identities: identities, limiter: limiter, logger: logger}
}

// GetTwoFactorStatus describes the caller's second factor.
func (h *TwoFactorHandler) GetTwoFactorStatus(ctx context.Context, _ *connect.Request[webv1.GetTwoFactorStatusRequest]) (*connect.Response[webv1.GetTwoFactorStatusResponse], error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	status, err := h.svc.Status(ctx, s.User)
	if err != nil {
		return nil, h.fail(ctx, "two-factor status", err)
	}
	res := &webv1.GetTwoFactorStatusResponse{TotpEnabled: status.Enabled, RecoveryCodesLeft: int32(status.RecoveryCodesLeft)} //nolint:gosec // G115: at most 10
	if s.Org != (tenant.OrgID{}) {
		caller := access.Caller{Principal: authz.Principal{User: s.User, Org: s.Org}}
		if res.RequiredByOrganization, err = h.svc.OrganizationRequirement(ctx, caller); err != nil {
			return nil, h.fail(ctx, "two-factor status", err)
		}
	}
	return connect.NewResponse(res), nil
}

// BeginTotpSetup creates a new secret.
func (h *TwoFactorHandler) BeginTotpSetup(ctx context.Context, _ *connect.Request[webv1.BeginTotpSetupRequest]) (*connect.Response[webv1.BeginTotpSetupResponse], error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	// The lockout state of codes lives with the password; accounts from OIDC use passkeys (M3-28).
	if hasPassword, err := h.identities.HasPassword(ctx, s.User); err != nil {
		return nil, h.fail(ctx, "load credentials", err)
	} else if !hasPassword {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errTOTPNeedsPassword)
	}
	profile, _, err := h.identities.Profile(ctx, s.User)
	if err != nil {
		return nil, h.fail(ctx, "load profile", err)
	}
	enrollment, err := h.svc.Begin(ctx, s.User, profile.Email, s.StepUpAt)
	if err != nil {
		return nil, h.fail(ctx, "begin TOTP setup", err)
	}
	return connect.NewResponse(&webv1.BeginTotpSetupResponse{Secret: enrollment.Secret, Uri: enrollment.URI}), nil
}

// ConfirmTotpSetup activates TOTP and returns the recovery codes.
func (h *TwoFactorHandler) ConfirmTotpSetup(ctx context.Context, req *connect.Request[webv1.ConfirmTotpSetupRequest]) (*connect.Response[webv1.ConfirmTotpSetupResponse], error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	if !h.limiter.Allow(s.User.String()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyCodes)
	}
	codes, err := h.svc.Confirm(ctx, s.User, req.Msg.GetCode())
	if err != nil {
		return nil, h.fail(ctx, "confirm TOTP setup", err)
	}
	return connect.NewResponse(&webv1.ConfirmTotpSetupResponse{RecoveryCodes: codes}), nil
}

// RegenerateRecoveryCodes replaces the recovery codes.
func (h *TwoFactorHandler) RegenerateRecoveryCodes(ctx context.Context, _ *connect.Request[webv1.RegenerateRecoveryCodesRequest]) (*connect.Response[webv1.RegenerateRecoveryCodesResponse], error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	codes, err := h.svc.RegenerateRecoveryCodes(ctx, s.User, s.StepUpAt)
	if err != nil {
		return nil, h.fail(ctx, "regenerate recovery codes", err)
	}
	return connect.NewResponse(&webv1.RegenerateRecoveryCodesResponse{RecoveryCodes: codes}), nil
}

// DisableTwoFactor removes the second factor.
func (h *TwoFactorHandler) DisableTwoFactor(ctx context.Context, _ *connect.Request[webv1.DisableTwoFactorRequest]) (*connect.Response[webv1.DisableTwoFactorResponse], error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	if err := h.svc.Disable(ctx, s.User, s.StepUpAt); err != nil {
		return nil, h.fail(ctx, "disable two-factor authentication", err)
	}
	return connect.NewResponse(&webv1.DisableTwoFactorResponse{}), nil
}

// GetSecurityPolicy returns the settings of the active organization.
func (h *TwoFactorHandler) GetSecurityPolicy(ctx context.Context, _ *connect.Request[webv1.GetSecurityPolicyRequest]) (*connect.Response[webv1.GetSecurityPolicyResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	required, err := h.svc.OrganizationRequirement(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "security policy", err)
	}
	return connect.NewResponse(&webv1.GetSecurityPolicyResponse{SecondFactorRequired: required}), nil
}

// SetSecondFactorRequired changes the requirement of the active organization.
func (h *TwoFactorHandler) SetSecondFactorRequired(ctx context.Context, req *connect.Request[webv1.SetSecondFactorRequiredRequest]) (*connect.Response[webv1.SetSecondFactorRequiredResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SetOrganizationRequirement(ctx, c, req.Msg.GetRequired()); err != nil {
		return nil, h.fail(ctx, "set second-factor requirement", err)
	}
	return connect.NewResponse(&webv1.SetSecondFactorRequiredResponse{}), nil
}

func (h *TwoFactorHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, twofactor.ErrAlreadyEnabled):
		return connect.NewError(connect.CodeAlreadyExists, twofactor.ErrAlreadyEnabled)
	case errors.Is(err, twofactor.ErrInvalidCode):
		return connect.NewError(connect.CodePermissionDenied, twofactor.ErrInvalidCode)
	case errors.Is(err, twofactor.ErrNoEnrollment):
		return connect.NewError(connect.CodeFailedPrecondition, twofactor.ErrNoEnrollment)
	case errors.Is(err, twofactor.ErrNotEnabled):
		return connect.NewError(connect.CodeFailedPrecondition, twofactor.ErrNotEnabled)
	case errors.Is(err, twofactor.ErrOwnSecondFactorFirst):
		return connect.NewError(connect.CodeFailedPrecondition, twofactor.ErrOwnSecondFactorFirst)
	}
	return internalError(ctx, h.logger, op, err)
}
