package webapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/selfmonitor"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// SessionCookie is the name of the session cookie. The __Host- prefix forces Secure, Path=/ and
// no Domain attribute in browsers, so the cookie cannot be set or read by sibling domains.
const SessionCookie = "__Host-sermon_session"

// IdentityReader resolves the profile and organizations of a user for the session view.
type IdentityReader interface {
	Profile(ctx context.Context, user identity.UserID) (auth.Profile, bool, error)
	Organizations(ctx context.Context, user identity.UserID) ([]auth.Organization, error)
	// HasPassword tells whether the user signs in with a password (M3-22).
	HasPassword(ctx context.Context, user identity.UserID) (bool, error)
}

// SecondFactorView tells the session view about the user's second factor (twofactor.Service).
type SecondFactorView interface {
	Enabled(ctx context.Context, user identity.UserID) (bool, error)
	TOTPEnabled(ctx context.Context, user identity.UserID) (bool, error)
	SetupRequired(ctx context.Context, org tenant.OrgID, user identity.UserID) (bool, error)
}

// AuthHandler implements sermon.web.v1.AuthService.
type AuthHandler struct {
	super        SuperadminCheck
	problems     InstanceProblems
	login        *auth.Service
	sessions     *session.Manager
	identities   IdentityReader
	secondFactor SecondFactorView // nil: no second factors
	principals   PrincipalLoader  // nil: no rights in the session view
	oidc         *oidc.Service    // nil: no sign-in at an OIDC provider (M3-20)
	oidcName     string
	passkeys     *passkey.Service // nil: no passkeys (M3-23)
	logger       *slog.Logger
}

// WithPrincipals lets the session view describe the caller's rights (navigation in the UI).
func (h *AuthHandler) WithPrincipals(principals PrincipalLoader) *AuthHandler {
	h.principals = principals
	return h
}

// SuperadminCheck tells superadmins apart (postgres.AccessStore).
type SuperadminCheck interface {
	IsSuperadmin(ctx context.Context, user identity.UserID) (bool, error)
}

// InstanceProblems are the self-monitoring's current problems (selfmonitor.Monitor).
type InstanceProblems interface {
	Problems() []selfmonitor.Problem
}

// WithInstanceHealth shows superadmins the server's own problems in their session (M2-24).
func (h *AuthHandler) WithInstanceHealth(super SuperadminCheck, problems InstanceProblems) *AuthHandler {
	h.super, h.problems = super, problems
	return h
}

// describeInstance marks superadmins and gives them the self-monitoring problems; nobody else sees them.
func (h *AuthHandler) describeInstance(ctx context.Context, s session.Session, view *webv1.Session) error {
	if h.super == nil {
		return nil
	}
	super, err := h.super.IsSuperadmin(ctx, s.User)
	if err != nil {
		return fmt.Errorf("superadmin: %w", err)
	}
	if !super {
		return nil
	}
	view.Superadmin = true
	if h.problems == nil {
		return nil
	}
	for _, p := range h.problems.Problems() {
		view.InstanceProblems = append(view.InstanceProblems, &webv1.InstanceProblem{
			Check: string(p.Check), Level: p.Level.String(), Since: timestamppb.New(p.Since), Value: p.Value,
		})
	}
	return nil
}

var _ webv1connect.AuthServiceHandler = (*AuthHandler)(nil)

// NewAuthHandler returns the AuthService handler.
func NewAuthHandler(login *auth.Service, sessions *session.Manager, identities IdentityReader, secondFactor SecondFactorView, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{login: login, sessions: sessions, identities: identities, secondFactor: secondFactor, logger: logger}
}

// Login checks the credentials and sets the session cookie.
func (h *AuthHandler) Login(ctx context.Context, req *connect.Request[webv1.LoginRequest]) (*connect.Response[webv1.LoginResponse], error) {
	result, err := h.login.Login(ctx, req.Msg.GetEmail(), req.Msg.GetPassword(), ClientIPFrom(ctx))
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return nil, connect.NewError(connect.CodeUnauthenticated, auth.ErrInvalidCredentials)
	case errors.Is(err, auth.ErrTooManyAttempts):
		return nil, connect.NewError(connect.CodeResourceExhausted, auth.ErrTooManyAttempts)
	case err != nil:
		return nil, h.internal(ctx, "login", err)
	}
	if result.SecondFactorRequired {
		res := connect.NewResponse(&webv1.LoginResponse{SecondFactorRequired: true})
		res.Header().Add("Set-Cookie", sessionCookie(result.Token, session.PendingTimeout).String())
		return res, nil
	}
	view, err := h.view(ctx, result.Session)
	if err != nil {
		return nil, h.internal(ctx, "login session view", err)
	}
	res := connect.NewResponse(&webv1.LoginResponse{Session: view})
	res.Header().Add("Set-Cookie", sessionCookie(result.Token, session.AbsoluteTimeout).String())
	return res, nil
}

// Logout ends the session and clears the cookie (also without a valid session).
func (h *AuthHandler) Logout(ctx context.Context, req *connect.Request[webv1.LogoutRequest]) (*connect.Response[webv1.LogoutResponse], error) {
	if token, ok := tokenFrom(req.Header()); ok {
		if err := h.sessions.Revoke(ctx, token); err != nil {
			return nil, h.internal(ctx, "logout", err)
		}
	}
	res := connect.NewResponse(&webv1.LogoutResponse{})
	res.Header().Add("Set-Cookie", sessionCookie("", -1).String())
	return res, nil
}

// GetSession describes the current session.
func (h *AuthHandler) GetSession(ctx context.Context, req *connect.Request[webv1.GetSessionRequest]) (*connect.Response[webv1.GetSessionResponse], error) {
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	s, err := h.sessions.Validate(ctx, token)
	if errors.Is(err, session.ErrInvalid) {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	if err != nil {
		return nil, h.internal(ctx, "validate session", err)
	}
	view, err := h.view(ctx, s)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	return connect.NewResponse(&webv1.GetSessionResponse{Session: view}), nil
}

// ErrNotAMember means the user does not belong to the requested organization.
var ErrNotAMember = errors.New("organization not found")

// SwitchOrganization moves the session to another organization of the user.
func (h *AuthHandler) SwitchOrganization(ctx context.Context, req *connect.Request[webv1.SwitchOrganizationRequest]) (*connect.Response[webv1.SwitchOrganizationResponse], error) {
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	current, err := h.sessions.Validate(ctx, token)
	if errors.Is(err, session.ErrInvalid) {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	if err != nil {
		return nil, h.internal(ctx, "validate session", err)
	}
	target, err := uuid.Parse(req.Msg.GetOrganizationId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidID)
	}
	orgs, err := h.identities.Organizations(ctx, current.User)
	if err != nil {
		return nil, h.internal(ctx, "load organizations", err)
	}
	if !slices.ContainsFunc(orgs, func(o auth.Organization) bool { return o.ID == tenant.OrgID(target) }) {
		return nil, connect.NewError(connect.CodeNotFound, ErrNotAMember)
	}
	newToken, moved, err := h.sessions.SwitchOrganization(ctx, token, tenant.OrgID(target))
	if err != nil {
		return nil, h.internal(ctx, "switch organization", err)
	}
	view, err := h.view(ctx, moved)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	res := connect.NewResponse(&webv1.SwitchOrganizationResponse{Session: view})
	remaining := time.Until(moved.CreatedAt.Add(session.AbsoluteTimeout))
	res.Header().Add("Set-Cookie", sessionCookie(newToken, max(remaining, time.Second)).String())
	return res, nil
}

// VerifySecondFactor completes a login that waits for the second factor.
func (h *AuthHandler) VerifySecondFactor(ctx context.Context, req *connect.Request[webv1.VerifySecondFactorRequest]) (*connect.Response[webv1.VerifySecondFactorResponse], error) {
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	result, err := h.login.VerifySecondFactor(ctx, token, req.Msg.GetCode(), ClientIPFrom(ctx))
	if err != nil {
		return nil, h.factorFailure(ctx, "verify second factor", err)
	}
	view, err := h.view(ctx, result.Session)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	res := connect.NewResponse(&webv1.VerifySecondFactorResponse{Session: view})
	remaining := time.Until(result.Session.CreatedAt.Add(session.AbsoluteTimeout))
	res.Header().Add("Set-Cookie", sessionCookie(result.Token, max(remaining, time.Second)).String())
	return res, nil
}

// ConfirmSecondFactor renews the step-up with a code.
func (h *AuthHandler) ConfirmSecondFactor(ctx context.Context, req *connect.Request[webv1.ConfirmSecondFactorRequest]) (*connect.Response[webv1.ConfirmSecondFactorResponse], error) {
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	s, err := h.login.ConfirmSecondFactor(ctx, token, req.Msg.GetCode(), ClientIPFrom(ctx))
	if err != nil {
		return nil, h.factorFailure(ctx, "confirm second factor", err)
	}
	view, err := h.view(ctx, s)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	return connect.NewResponse(&webv1.ConfirmSecondFactorResponse{Session: view}), nil
}

func (h *AuthHandler) factorFailure(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, session.ErrInvalid):
		return connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	case errors.Is(err, session.ErrNotPending):
		return connect.NewError(connect.CodeFailedPrecondition, session.ErrNotPending)
	case errors.Is(err, auth.ErrWrongCode):
		return connect.NewError(connect.CodePermissionDenied, auth.ErrWrongCode)
	case errors.Is(err, auth.ErrTooManyAttempts):
		return connect.NewError(connect.CodeResourceExhausted, auth.ErrTooManyAttempts)
	}
	return h.internal(ctx, op, err)
}

// secondFactorSetupRequired is the error of tenant services while the organization requires a
// second factor the user doesn't have; the detail lets the UI open the setup.
func secondFactorSetupRequired() *connect.Error {
	err := connect.NewError(connect.CodeFailedPrecondition, access.ErrSecondFactorSetupRequired)
	if detail, detailErr := connect.NewErrorDetail(&webv1.SecondFactorSetupRequired{}); detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

// stepUpRequired is the error of sensitive actions without a recent re-authentication; the
// StepUpRequired detail lets clients ask for the password and retry.
func stepUpRequired() *connect.Error {
	err := connect.NewError(connect.CodeFailedPrecondition, access.ErrStepUpRequired)
	if detail, detailErr := connect.NewErrorDetail(&webv1.StepUpRequired{}); detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

// ConfirmPassword renews the step-up of the current session with the user's password.
func (h *AuthHandler) ConfirmPassword(ctx context.Context, req *connect.Request[webv1.ConfirmPasswordRequest]) (*connect.Response[webv1.ConfirmPasswordResponse], error) {
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	s, err := h.login.ConfirmPassword(ctx, token, req.Msg.GetPassword(), ClientIPFrom(ctx))
	switch {
	case errors.Is(err, session.ErrInvalid):
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	case errors.Is(err, auth.ErrWrongPassword):
		return nil, connect.NewError(connect.CodePermissionDenied, auth.ErrWrongPassword)
	case errors.Is(err, auth.ErrSecondFactorRequired):
		return nil, connect.NewError(connect.CodeFailedPrecondition, auth.ErrSecondFactorRequired)
	case errors.Is(err, auth.ErrTooManyAttempts):
		return nil, connect.NewError(connect.CodeResourceExhausted, auth.ErrTooManyAttempts)
	case err != nil:
		return nil, h.internal(ctx, "confirm password", err)
	}
	view, err := h.view(ctx, s)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	return connect.NewResponse(&webv1.ConfirmPasswordResponse{Session: view}), nil
}

func (h *AuthHandler) view(ctx context.Context, s session.Session) (*webv1.Session, error) {
	profile, _, err := h.identities.Profile(ctx, s.User)
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}
	orgs, err := h.identities.Organizations(ctx, s.User)
	if err != nil {
		return nil, fmt.Errorf("load organizations: %w", err)
	}
	view := &webv1.Session{
		UserId: s.User.String(), Email: profile.Email, DisplayName: profile.DisplayName,
		StepUpUntil: timestamppb.New(s.StepUpAt.Add(access.StepUpMaxAge)),
	}
	if s.Org != (tenant.OrgID{}) {
		view.ActiveOrganizationId = s.Org.String()
	}
	for _, o := range orgs {
		view.Organizations = append(view.Organizations, &webv1.Organization{Id: o.ID.String(), Name: o.Name})
	}
	if err := h.describeInstance(ctx, s, view); err != nil {
		return nil, err
	}
	if err := h.describeRights(ctx, s, view); err != nil {
		return nil, err
	}
	if view.HasPassword, err = h.identities.HasPassword(ctx, s.User); err != nil {
		return nil, fmt.Errorf("password: %w", err)
	}
	if h.oidc != nil {
		if _, view.OidcLinked, err = h.oidc.SignInWays(ctx, s.User); err != nil {
			return nil, fmt.Errorf("sign-in ways: %w", err)
		}
	}
	if h.passkeys.Available() {
		view.PasskeysAvailable = true
		n, err := h.passkeys.Count(ctx, s.User)
		if err != nil {
			return nil, fmt.Errorf("passkeys: %w", err)
		}
		view.PasskeyCount = uint32(min(n, passkey.MaxPerUser)) //nolint:gosec // G115: at most 10
	}
	if h.secondFactor != nil {
		if view.SecondFactorEnabled, err = h.secondFactor.Enabled(ctx, s.User); err != nil {
			return nil, fmt.Errorf("second factor: %w", err)
		}
		if view.TotpEnabled, err = h.secondFactor.TOTPEnabled(ctx, s.User); err != nil {
			return nil, fmt.Errorf("second factor: %w", err)
		}
		// A login with the provider's second factor meets the requirement (as access.Loader).
		if s.Org != (tenant.OrgID{}) && !view.GetSecondFactorEnabled() && !s.ExternalMFA {
			if view.SecondFactorSetupRequired, err = h.secondFactor.SetupRequired(ctx, s.Org, s.User); err != nil {
				return nil, fmt.Errorf("second-factor requirement: %w", err)
			}
		}
	}
	return view, nil
}

// describeRights fills the caller's rights in the active organization. Without them (no
// organization, removed member, second factor missing) the view simply has none.
func (h *AuthHandler) describeRights(ctx context.Context, s session.Session, view *webv1.Session) error {
	if h.principals == nil || s.Org == (tenant.OrgID{}) {
		return nil
	}
	p, err := h.principals.Load(ctx, s)
	if errors.Is(err, access.ErrNoMembership) || errors.Is(err, access.ErrSecondFactorSetupRequired) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load rights: %w", err)
	}
	policy := authz.NewPolicy()
	view.OrgAdmin = policy.Allowed(p, authz.ManageMembers, authz.InOrg(p.Org))
	view.EnrollGroupIds = groupsFor(policy, p, authz.CreateEnrollmentTokens)
	view.SilenceGroupIds = groupsFor(policy, p, authz.SilenceAlerts)
	view.NodeIdentityGroupIds = groupsFor(policy, p, authz.UnlockNode)
	view.TagGroupIds = groupsFor(policy, p, authz.TagNodes)
	view.RunUpdatesGroupIds = groupsFor(policy, p, authz.RunUpdates)
	view.RebootGroupIds = groupsFor(policy, p, authz.RebootNodes)
	return nil
}

// groupsFor lists the groups where the principal may do action; "*" stands for all groups.
func groupsFor(policy authz.Policy, p authz.Principal, action authz.Action) []string {
	scope, err := policy.ScopeForAction(p, action)
	if err != nil {
		return nil
	}
	if scope.AllGroups() {
		return []string{"*"}
	}
	var out []string
	for _, g := range scope.Groups() {
		out = append(out, uuid.UUID(g).String())
	}
	return out
}

// internal logs the cause with a correlation ID and returns a generic error to the client.
func (h *AuthHandler) internal(ctx context.Context, op string, err error) error {
	return internalError(ctx, h.logger, op, err)
}

// internalError logs the cause with a correlation ID and returns a generic error to the client.
func internalError(ctx context.Context, logger *slog.Logger, op string, err error) error {
	ref := correlationID()
	logger.ErrorContext(ctx, "request failed", "op", op, "ref", ref, "error", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error (ref "+ref+")"))
}

func sessionCookie(token string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	return c
}

func cookieHeaderOnly(cookie string) http.Header {
	return http.Header{"Cookie": []string{cookie}}
}

func tokenFrom(h http.Header) (string, bool) {
	c, err := (&http.Request{Header: h}).Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

func correlationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
