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

// Config holds Salesforce OAuth credentials (Username-Password flow).
type Config struct {
	ClientID     string
	ClientSecret string
	Username     string
	// Password should already have the security token appended: password+token
	Password    string
	InstanceURL string
}

// Client is a minimal Salesforce REST API client.
type Client struct {
	cfg         Config
	httpClient  *http.Client
	accessToken string
	apiBase     string // e.g. https://yourorg.my.salesforce.com
}

func New(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	InstanceURL string `json:"instance_url"`
	TokenType   string `json:"token_type"`
}

func (c *Client) authenticate() error {
	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", c.cfg.ClientID)
	data.Set("client_secret", c.cfg.ClientSecret)
	data.Set("username", c.cfg.Username)
	data.Set("password", c.cfg.Password)

	tokenURL := c.cfg.InstanceURL + "/services/oauth2/token"
	resp, err := c.httpClient.PostForm(tokenURL, data)
	if err != nil {
		return fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oauth error %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("parsing token response: %w", err)
	}

	c.accessToken = tr.AccessToken
	c.apiBase = tr.InstanceURL
	log.Printf("Salesforce authenticated, instance: %s", c.apiBase)
	return nil
}

// ensureAuth authenticates if we don't have a token yet.
func (c *Client) ensureAuth() error {
	if c.accessToken == "" {
		return c.authenticate()
	}
	return nil
}

// ---------------------------------------------------------------------------
// SOQL query helper
// ---------------------------------------------------------------------------

type queryResponse[T any] struct {
	TotalSize int    `json:"totalSize"`
	Done      bool   `json:"done"`
	Records   []T    `json:"records"`
	NextURL   string `json:"nextRecordsUrl"`
}

func query[T any](c *Client, soql string) ([]T, error) {
	if err := c.ensureAuth(); err != nil {
		return nil, err
	}

	endpoint := c.apiBase + "/services/data/v59.0/query?q=" + url.QueryEscape(soql)
	var all []T

	for endpoint != "" {
		req, _ := http.NewRequest("GET", endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("SOQL query failed: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Re-auth once on 401
		if resp.StatusCode == http.StatusUnauthorized {
			c.accessToken = ""
			if err := c.authenticate(); err != nil {
				return nil, err
			}
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
		endpoint = c.apiBase + page.NextURL
	}

	return all, nil
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// OpportunityRecord maps to a Salesforce Opportunity row returned by SOQL.
type OpportunityRecord struct {
	ID          string     `json:"Id"`
	Name        string     `json:"Name"`
	AccountName string     `json:"-"` // populated from nested Account
	AccountObj  *sfAccount `json:"Account"`
	StageName   string     `json:"StageName"`
	CloseDate   string     `json:"CloseDate"`
	CreatedDate string     `json:"CreatedDate"`
	// ARR__c lives directly on the opportunity
	ARR *float64 `json:"ARR__c"`
	// CurrencyIsoCode present when multi-currency is enabled
	CurrencyIsoCode string `json:"CurrencyIsoCode"`
}

type sfAccount struct {
	Name string `json:"Name"`
}

// QuoteLineItemRecord maps to SFDC QuoteLineItem rows.
type QuoteLineItemRecord struct {
	ID string `json:"Id"`
	// Ruby__DeltaARR__c is the ARR delta field on quote line items
	DeltaARR *float64 `json:"Ruby__DeltaARR__c"`
	// Link back to opportunity via Quote
	QuoteObj *sfQuote `json:"Quote"`
}

type sfQuote struct {
	OpportunityID string `json:"OpportunityId"`
}

// Contract is our unified domain object stored in Postgres.
type Contract struct {
	SalesforceID   string
	AccountName    string
	DealName       string
	StageName      string
	CloseDate      *time.Time
	ARR            float64 // from Opportunity.ARR__c
	DeltaARR       float64 // sum of QuoteLineItem.Ruby__DeltaARR__c
	CurrencyCode   string
	LastModifiedAt time.Time
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// FetchOpportunities returns all Closed Won opportunities with ARR data.
// If sinceTime is non-zero, only records modified after that time are returned.
func (c *Client) FetchOpportunities(sinceTime time.Time) ([]Contract, error) {
	var soql string

	baseFields := strings.Join([]string{
		"Id", "Name", "Account.Name", "StageName",
		"CloseDate", "CreatedDate", "ARR__c", "CurrencyIsoCode",
		"LastModifiedDate",
	}, ", ")

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

	opps, err := query[rawOpp](c, soql)
	if err != nil {
		return nil, fmt.Errorf("fetching opportunities: %w", err)
	}

	// Build a map for DeltaARR sums (keyed by OpportunityId)
	deltaByOpp, err := c.fetchDeltaARR(sinceTime)
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

// fetchDeltaARR queries QuoteLineItems and sums Ruby__DeltaARR__c by OpportunityId.
func (c *Client) fetchDeltaARR(sinceTime time.Time) (map[string]float64, error) {
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

	items, err := query[rawQLI](c, soql)
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
