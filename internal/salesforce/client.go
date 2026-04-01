package salesforce

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds Salesforce OAuth Web Server flow credentials.
type Config struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
	InstanceURL  string // e.g. https://login.salesforce.com or https://test.salesforce.com
}

// Client is a Salesforce REST API client bound to a specific user's tokens.
type Client struct {
	cfg          Config
	httpClient   *http.Client
	AccessToken  string
	RefreshToken string
	APIBase      string // e.g. https://yourorg.my.salesforce.com
}

func New(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewWithTokens creates a Client already loaded with user tokens.
func NewWithTokens(cfg Config, accessToken, refreshToken, apiBase string) *Client {
	return &Client{
		cfg:          cfg,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		APIBase:      apiBase,
	}
}

// ---------------------------------------------------------------------------
// OAuth Web Server Flow
// ---------------------------------------------------------------------------

// AuthCodeURL returns the Salesforce authorization URL to redirect the user to.
func (c *Client) AuthCodeURL(state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", c.cfg.ClientID)
	params.Set("redirect_uri", c.cfg.CallbackURL)
	params.Set("state", state)
	return c.cfg.InstanceURL + "/services/oauth2/authorize?" + params.Encode()
}

// TokenResponse holds the token exchange response from Salesforce.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	InstanceURL  string `json:"instance_url"`
	TokenType    string `json:"token_type"`
	IssuedAt     string `json:"issued_at"`
}

// ExchangeCode exchanges an authorization code for access + refresh tokens.
func (c *Client) ExchangeCode(code string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", c.cfg.ClientID)
	data.Set("client_secret", c.cfg.ClientSecret)
	data.Set("redirect_uri", c.cfg.CallbackURL)
	data.Set("code", code)
	return c.postToken(data)
}

// RefreshAccessToken uses the refresh token to get a new access token.
func (c *Client) RefreshAccessToken(refreshToken string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", c.cfg.ClientID)
	data.Set("client_secret", c.cfg.ClientSecret)
	data.Set("refresh_token", refreshToken)
	return c.postToken(data)
}

func (c *Client) postToken(data url.Values) (*TokenResponse, error) {
	tokenURL := c.cfg.InstanceURL + "/services/oauth2/token"
	resp, err := c.httpClient.PostForm(tokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token error %d: %s", resp.StatusCode, body)
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	return &tr, nil
}

// ---------------------------------------------------------------------------
// SOQL query helper
// ---------------------------------------------------------------------------

type queryResponse[T any] struct {
	TotalSize int    `json:"totalSize"`
	Done      bool   `json:"done"`
	NextURL   string `json:"nextRecordsUrl"`
	Records   []T    `json:"records"`
}

func queryWith[T any](c *Client, soql string) ([]T, error) {
	endpoint := c.APIBase + "/services/data/v59.0/query?q=" + url.QueryEscape(soql)
	var all []T

	for endpoint != "" {
		req, _ := http.NewRequest("GET", endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("SOQL query failed: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Try to refresh once on 401
		if resp.StatusCode == http.StatusUnauthorized && c.RefreshToken != "" {
			tr, err := c.RefreshAccessToken(c.RefreshToken)
			if err != nil {
				return nil, fmt.Errorf("token refresh failed: %w", err)
			}
			c.AccessToken = tr.AccessToken
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("SOQL %d: %s", resp.StatusCode, body)
		}

		var page queryResponse[T]
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parsing query response: %w", err)
		}
		all = append(all, page.Records...)

		if page.Done || page.NextURL == "" {
			break
		}
		endpoint = c.APIBase + page.NextURL
	}

	return all, nil
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

type sfAccount struct {
	Name string `json:"Name"`
}

// Contract is the unified domain object stored in Postgres.
type Contract struct {
	SalesforceID   string
	AccountName    string
	DealName       string
	StageName      string
	CloseDate      *time.Time
	ARR            float64
	DeltaARR       float64
	CurrencyCode   string
	LastModifiedAt time.Time
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

// FetchOpportunities returns Closed Won opportunities with ARR data.
func FetchOpportunities(c *Client, sinceTime time.Time) ([]Contract, error) {
	baseFields := strings.Join([]string{
		"Id", "Name", "Account.Name", "StageName",
		"CloseDate", "ARR__c", "CurrencyIsoCode", "LastModifiedDate",
	}, ", ")

	var soql string
	if sinceTime.IsZero() {
		soql = fmt.Sprintf(
			"SELECT %s FROM Opportunity WHERE StageName = 'Closed Won' ORDER BY LastModifiedDate DESC",
			baseFields,
		)
	} else {
		soql = fmt.Sprintf(
			"SELECT %s FROM Opportunity WHERE StageName = 'Closed Won' AND LastModifiedDate > %s ORDER BY LastModifiedDate DESC",
			baseFields,
			sinceTime.UTC().Format("2006-01-02T15:04:05Z"),
		)
	}

	type rawOpp struct {
		ID               string     `json:"Id"`
		Name             string     `json:"Name"`
		Account          *sfAccount `json:"Account"`
		StageName        string     `json:"StageName"`
		CloseDate        string     `json:"CloseDate"`
		ARR              *float64   `json:"ARR__c"`
		CurrencyIsoCode  string     `json:"CurrencyIsoCode"`
		LastModifiedDate string     `json:"LastModifiedDate"`
	}

	opps, err := queryWith[rawOpp](c, soql)
	if err != nil {
		return nil, fmt.Errorf("fetching opportunities: %w", err)
	}

	deltaByOpp, err := fetchDeltaARR(c, sinceTime)
	if err != nil {
		log.Printf("WARN: could not fetch QuoteLineItem DeltaARR: %v", err)
		deltaByOpp = map[string]float64{}
	}

	contracts := make([]Contract, 0, len(opps))
	for _, o := range opps {
		var arr float64
		if o.ARR != nil {
			arr = *o.ARR
		}

		var closeDate *time.Time
		if o.CloseDate != "" {
			t, err := time.Parse("2006-01-02", o.CloseDate)
			if err == nil {
				closeDate = &t
			}
		}

		var lastMod time.Time
		if o.LastModifiedDate != "" {
			lastMod, _ = time.Parse(time.RFC3339, o.LastModifiedDate)
		}

		accountName := ""
		if o.Account != nil {
			accountName = o.Account.Name
		}

		contracts = append(contracts, Contract{
			SalesforceID:   o.ID,
			AccountName:    accountName,
			DealName:       o.Name,
			StageName:      o.StageName,
			CloseDate:      closeDate,
			ARR:            arr,
			DeltaARR:       deltaByOpp[o.ID],
			CurrencyCode:   o.CurrencyIsoCode,
			LastModifiedAt: lastMod,
		})
	}

	return contracts, nil
}

func fetchDeltaARR(c *Client, sinceTime time.Time) (map[string]float64, error) {
	var soql string
	if sinceTime.IsZero() {
		soql = "SELECT Id, Ruby__DeltaARR__c, Quote.OpportunityId FROM QuoteLineItem WHERE Quote.Opportunity.StageName = 'Closed Won'"
	} else {
		soql = fmt.Sprintf(
			"SELECT Id, Ruby__DeltaARR__c, Quote.OpportunityId FROM QuoteLineItem WHERE Quote.Opportunity.StageName = 'Closed Won' AND LastModifiedDate > %s",
			sinceTime.UTC().Format("2006-01-02T15:04:05Z"),
		)
	}

	type rawQLI struct {
		ID       string   `json:"Id"`
		DeltaARR *float64 `json:"Ruby__DeltaARR__c"`
		Quote    *struct {
			OpportunityID string `json:"OpportunityId"`
		} `json:"Quote"`
	}

	items, err := queryWith[rawQLI](c, soql)
	if err != nil {
		return nil, err
	}

	result := map[string]float64{}
	for _, item := range items {
		if item.Quote == nil || item.DeltaARR == nil {
			continue
		}
		result[item.Quote.OpportunityID] += *item.DeltaARR
	}
	return result, nil
}
