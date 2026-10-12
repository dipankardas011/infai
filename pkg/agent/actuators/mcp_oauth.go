package actuators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// mcpOAuthCallbackPath is the path an authorization server redirects back to
// when the configured redirect URL names a port but no path.
const mcpOAuthCallbackPath = "/oauth/callback"

// errAuthRequired is what a session reports when the credential it was handed is
// missing, expired or insufficient. It never leaves this package: a caller sees
// it as the "mcp_unauthorized" tool error.
var errAuthRequired = errors.New("MCP authorization is missing, expired or insufficient; run infaiw mcp refresh and reopen the session")

// mcpSessionAuth presents the credential a session was given and nothing else.
// Only `infaiw mcp add` and `infaiw mcp refresh` authorize, so a session never
// starts a flow of its own: an expired or missing token is reported as an
// authorization error instead. § MCP management user story.
type mcpSessionAuth struct {
	token *oauth2.Token
}

func (h *mcpSessionAuth) TokenSource(context.Context) (oauth2.TokenSource, error) {
	if h.token == nil {
		return nil, nil
	}
	return h, nil
}

func (h *mcpSessionAuth) Token() (*oauth2.Token, error) {
	if h.token.AccessToken == "" || (!h.token.Expiry.IsZero() && !time.Now().Before(h.token.Expiry)) {
		return nil, errAuthRequired
	}
	return h.token, nil
}

func (*mcpSessionAuth) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	response.Body.Close()
	return errAuthRequired
}

// MCPAuthUpdate is one step of an MCP authorization: the loopback callback the
// flow listens on, and the URL the operator has to open. Both are reported, so a
// refusal that names a redirect_uri can be read against what was actually sent.
type MCPAuthUpdate struct {
	Callback string
	URL      string
}

// MCPAuthNotify reports one MCPAuthUpdate. An error from it aborts the
// authorization.
type MCPAuthNotify func(MCPAuthUpdate) error

// AuthorizeMCPOAuth runs one interactive authorization for a server and returns
// the credentials to store; the engine owns their persistence. The call blocks
// until the browser comes back, the flow fails, or the context ends.
func AuthorizeMCPOAuth(ctx context.Context, server contracts.MCPServerConfig, logger *slog.Logger, notify MCPAuthNotify) (*contracts.MCPOAuthConfig, error) {
	if server.OAuth == nil {
		return nil, errors.New("MCP OAuth is not configured for this server")
	}
	config := *server.OAuth
	if logger == nil || notify == nil {
		return nil, errors.New("MCP authorization needs a logger and an update callback")
	}
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	redirectURL, err := url.Parse(config.RedirectURL)
	if err != nil {
		return nil, errors.New("invalid MCP OAuth redirect URL")
	}
	if redirectURL.Scheme == "" {
		redirectURL.Scheme = "http"
	}
	if redirectURL.Scheme != "http" {
		return nil, errors.New("MCP OAuth redirect URL must be plain HTTP on a loopback address")
	}
	if host := redirectURL.Hostname(); host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		// The callback carries the authorization code, so it is only ever served
		// on the loopback interface.
		return nil, errors.New("MCP OAuth redirect URL must be a loopback address")
	}
	if redirectURL.Path == "" {
		redirectURL.Path = mcpOAuthCallbackPath
	}
	if redirectURL.Host == "" {
		redirectURL.Host = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", redirectURL.Host)
	if err != nil {
		return nil, fmt.Errorf("MCP OAuth cannot listen on %s: %w", redirectURL.Host, err)
	}
	defer listener.Close()
	redirectURL.Host = net.JoinHostPort("127.0.0.1", fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	redirect := redirectURL.String()

	var captured *contracts.MCPOAuthConfig
	handlerConfig := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirect,
		Client:      client,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			authURL, err := url.Parse(args.URL)
			if err != nil {
				return nil, errors.New("invalid MCP authorization URL")
			}
			state := authURL.Query().Get("state")
			if state == "" {
				// Without a state the callback cannot tell its own authorization
				// from anyone else's, and waiting for it would only end in a
				// timeout.
				return nil, errors.New("MCP authorization URL carries no state")
			}
			results := make(chan *auth.AuthorizationResult, 1)
			failures := make(chan error, 1)
			mux := http.NewServeMux()
			mux.HandleFunc(redirectURL.Path, func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				if r.Method != http.MethodGet || query.Get("state") != state {
					http.Error(w, "No authorization is waiting for this response.", http.StatusBadRequest)
					return
				}
				if code := query.Get("error"); code != "" {
					// The authorization server's own words: they are what tells
					// the operator which registration or scope it refused.
					reason := code
					if description := query.Get("error_description"); description != "" {
						reason += ": " + description
					}
					http.Error(w, "Authorization refused: "+reason, http.StatusBadRequest)
					select {
					case failures <- fmt.Errorf("MCP authorization was refused: %s", reason):
					default:
					}
					// The browser must not be told the authorization succeeded:
					// the terminal is about to report the refusal.
					return
				}
				if query.Get("code") == "" {
					http.Error(w, "Missing authorization code.", http.StatusBadRequest)
					return
				}
				select {
				case results <- &auth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state"), Iss: query.Get("iss")}:
				default:
				}
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				fmt.Fprintln(w, "Authorization received. Return to the terminal.")
			})
			callbackServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			defer callbackServer.Close()
			go func() {
				if err := callbackServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					select {
					case failures <- errors.New("MCP OAuth callback listener failed"):
					default:
					}
				}
			}()
			if err := notify(MCPAuthUpdate{URL: args.URL}); err != nil {
				return nil, err
			}
			select {
			case result := <-results:
				return result, nil
			case err := <-failures:
				return nil, err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		NewTokenSource: func(_ context.Context, _ *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			config.AccessToken = token.AccessToken
			config.Expiry = token.Expiry
			captured = &config
			return oauth2.StaticTokenSource(token), nil
		},
	}
	// A client id is only usable with the redirect URL it was registered with.
	// An entry that pins none gets a loopback port invented here at every
	// authorization, and a registration made earlier cannot match it — that
	// mismatch is what an authorization server answers with "redirect_uri does
	// not match any of the OAuth 2.0 Client's pre-registered redirect urls". So
	// a pinned redirect URL with a client id authorizes as that client, and
	// everything else registers a client of its own for the URL in hand.
	if config.ClientID != "" && config.RedirectURL != "" {
		credentials := &oauthex.ClientCredentials{ClientID: config.ClientID, Issuer: config.Issuer}
		if config.ClientSecret != "" {
			credentials.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: config.ClientSecret}
		}
		handlerConfig.PreregisteredClient = credentials
	} else {
		if config.ClientID != "" {
			// The stored client cannot cover a callback this process only just
			// invented, and reusing it is what an authorization server refuses
			// with "redirect_uri does not match".
			logger.WarnContext(ctx, "stored MCP oauth clientId ignored: no redirectUrl pins the callback it was registered with", "endpoint", server.URL)
		}
		handlerConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{
			// A loopback callback makes this a native client (§ RFC 8252): the
			// type is what tells an authorization server that the redirect is a
			// local port, not the web origin a "web" client would need.
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:              mcpClientName,
				RedirectURIs:            []string{redirect},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code"},
				ApplicationType:         "native",
			},
		}
	}
	if len(config.Scopes) > 0 {
		handlerConfig.ScopeFilter = func([]string) []string { return slices.Clone(config.Scopes) }
	}
	registration := "dynamic client registration"
	if handlerConfig.PreregisteredClient != nil {
		registration = "preregistered client " + config.ClientID
	}
	logger.InfoContext(ctx, "MCP authorization starting", "endpoint", server.URL, "callback", redirect, "client", registration)
	if err := notify(MCPAuthUpdate{Callback: redirect}); err != nil {
		return nil, err
	}

	handler, err := auth.NewAuthorizationCodeHandler(handlerConfig)
	if err != nil {
		return nil, errors.New("invalid MCP OAuth handler configuration")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("MCP authorization endpoint could not be reached")
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(response.Body, webMaxBodyBytes), response.Body}
	if err := handler.Authorize(ctx, request, response); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("MCP authorization failed")
	}
	if captured == nil {
		return nil, errors.New("MCP authorization did not return a token")
	}
	return captured, nil
}
