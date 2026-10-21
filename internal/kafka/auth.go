package kafka

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/twmb/franz-go/pkg/sasl/aws"
	"github.com/twmb/franz-go/pkg/sasl/oauth"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/shepherd-yun/datavim/internal/db"
)

// OAuthConfig is SASL "oauth": the client credentials flow against TokenURL.
type OAuthConfig struct{ TokenURL, ClientID, ClientSecret, Scope string }

// AWSConfig is SASL "aws-msk-iam"; credentials come from the AWS SDK's
// chain (environment, ~/.aws, SSO). Empty fields are left to the SDK.
type AWSConfig struct{ Region, Profile string }

// ErrOAuthRefused is a token endpoint that refused the client secret.
var ErrOAuthRefused = errors.New("kafka: oauth client secret refused")

// oauthAuth hands kgo a token on every authentication. The token source
// caches the token and fetches a new one when it is about to expire; its
// HTTP requests go through dial, the brokers' tunnel.
func oauthAuth(cfg Config, dial db.DialFunc) func(context.Context) (oauth.Auth, error) {
	o := cfg.OAuth
	cc := clientcredentials.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, TokenURL: o.TokenURL}
	if o.Scope != "" {
		cc.Scopes = []string{o.Scope}
	}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dial}}
	// the token source keeps this context for every fetch, so it is not
	// a call's context
	ts := cc.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, hc))
	return func(context.Context) (oauth.Auth, error) {
		tok, err := ts.Token()
		if err != nil {
			return oauth.Auth{}, oauthErr(err)
		}
		return oauth.Auth{Token: tok.AccessToken}, nil
	}
}

// oauthErr is a token fetch's error without the response body, which an
// identity provider may fill with anything.
func oauthErr(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_client" || re.ErrorCode == "unauthorized_client" ||
			(re.Response != nil && (re.Response.StatusCode == http.StatusUnauthorized || re.Response.StatusCode == http.StatusForbidden)) {
			return ErrOAuthRefused
		}
		if re.Response != nil {
			return fmt.Errorf("kafka oauth token: %s", re.Response.Status)
		}
	}
	return fmt.Errorf("kafka oauth token: %w", err)
}

// awsAuth signs MSK IAM logins with the SDK chain's credentials, fetched
// on each login; the SDK's credentials cache refreshes expired ones.
func awsAuth(ctx context.Context, a AWSConfig) (func(context.Context) (aws.Auth, error), error) {
	var opts []func(*awsconfig.LoadOptions) error
	if a.Region != "" {
		opts = append(opts, awsconfig.WithRegion(a.Region))
	}
	if a.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(a.Profile))
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka aws-msk-iam: %w", err)
	}
	return func(ctx context.Context) (aws.Auth, error) {
		c, err := ac.Credentials.Retrieve(ctx)
		if err != nil {
			return aws.Auth{}, fmt.Errorf("kafka aws-msk-iam credentials: %w", err)
		}
		return aws.Auth{AccessKey: c.AccessKeyID, SecretKey: c.SecretAccessKey, SessionToken: c.SessionToken, UserAgent: "datavim"}, nil
	}, nil
}
