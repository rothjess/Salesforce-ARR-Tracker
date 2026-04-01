package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"time"

	dbpkg "github.com/coder/arr-tracker-sf/internal/db"
	"github.com/coder/arr-tracker-sf/internal/salesforce"
)

// Handler wires together DB, Salesforce config, and session store.
type Handler struct {
	db       *dbpkg.DB
	sfCfg    salesforce.Config
	sessions *sessionStore
}

func New(db *dbpkg.DB, sfCfg salesforce.Config, sessionSecret string) *Handler {
	return &Handler{
		db:       db,
		sfCfg:    sfCfg,
		sessions: newSessionStore(sessionSecret),
	}
}

// RegisterRoutes attaches all HTTP routes to mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Auth routes (no session required)
	mux.HandleFunc("/auth/login", h.authLogin)
	mux.HandleFunc("/auth/callback", h.authCallback)
	mux.HandleFunc("/auth/logout", h.authLogout)

	// API routes (session required)
	mux.HandleFunc("/api/health", h.requireAuth(h.health))
	mux.HandleFunc("/api/summary", h.requireAuth(h.summary))
	mux.HandleFunc("/api/contracts", h.requireAuth(h.contracts))
	mux.HandleFunc("/api/sync", h.requireAuth(h.sync))

	// Serve React frontend
	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))
}

// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	state := randomState()
	// Store state in a short-lived cookie for CSRF protection
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	sf := salesforce.New(h.sfCfg)
	http.Redirect(w, r, sf.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func (h *Handler) authCallback(w http.ResponseWriter, r *http.Request) {
	// Validate state
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	// Clear the state cookie
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", MaxAge: -1, Path: "/"})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	sf := salesforce.New(h.sfCfg)
	tr, err := sf.ExchangeCode(code)
	if err != nil {
		log.Printf("ERROR: token exchange failed: %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	// Store tokens in session
	sess := &session{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		APIBase:      tr.InstanceURL,
		CreatedAt:    time.Now(),
	}
	sessionID := h.sessions.create(sess)

	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   86400 * 30, // 30 days
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session_id"); err == nil {
		h.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "session_id", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// ---------------------------------------------------------------------------
// Session middleware
// ---------------------------------------------------------------------------

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session_id")
		if err != nil {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		if _, ok := h.sessions.get(c.Value); !ok {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// sfClient returns a Salesforce client for the current session.
func (h *Handler) sfClient(r *http.Request) *salesforce.Client {
	c, _ := r.Cookie("session_id")
	sess, _ := h.sessions.get(c.Value)
	return salesforce.NewWithTokens(h.sfCfg, sess.AccessToken, sess.RefreshToken, sess.APIBase)
}

// ---------------------------------------------------------------------------
// API handlers
// ---------------------------------------------------------------------------

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.db.Summary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s)
}

func (h *Handler) contracts(w http.ResponseWriter, r *http.Request) {
	stageFilter := r.URL.Query().Get("stage")
	if stageFilter == "" {
		stageFilter = "CLOSED_WON"
	}
	contracts, err := h.db.ListContracts(stageFilter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, contracts)
}

func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	sf := h.sfClient(r)
	full := r.URL.Query().Get("full") == "true"
	upserted, total, err := h.runSync(sf, !full)
	h.db.LogSync(upserted, total, !full, err) //nolint:errcheck

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"upserted":    upserted,
		"total":       total,
		"incremental": !full,
	})
}

// ---------------------------------------------------------------------------
// Sync logic
// ---------------------------------------------------------------------------

func (h *Handler) runSync(sf *salesforce.Client, incremental bool) (upserted, total int, err error) {
	var since time.Time
	if incremental {
		since, err = h.db.LastSyncTime()
		if err != nil {
			log.Printf("WARN: could not get last sync time, doing full sync: %v", err)
			since = time.Time{}
		}
	}

	log.Printf("Starting sync (incremental=%v, since=%v)", incremental, since)

	sfContracts, err := salesforce.FetchOpportunities(sf, since)
	if err != nil {
		return 0, 0, err
	}

	total = len(sfContracts)
	log.Printf("Fetched %d opportunities from Salesforce", total)

	dbContracts := make([]dbpkg.Contract, len(sfContracts))
	for i, c := range sfContracts {
		dbContracts[i] = dbpkg.Contract{
			SalesforceID:   c.SalesforceID,
			AccountName:    c.AccountName,
			DealName:       c.DealName,
			StageName:      c.StageName,
			CloseDate:      c.CloseDate,
			ARR:            c.ARR,
			DeltaARR:       c.DeltaARR,
			CurrencyCode:   c.CurrencyCode,
			LastModifiedAt: c.LastModifiedAt,
		}
	}

	upserted, err = h.db.UpsertContracts(dbContracts)
	return upserted, total, err
}

// StartScheduler runs a background sync every 24 hours.
// It requires a valid Salesforce client — call this after a user has logged in
// and you have tokens, or skip it and rely on manual syncs from the UI.
func (h *Handler) StartScheduler(sf *salesforce.Client) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			upserted, total, err := h.runSync(sf, true)
			h.db.LogSync(upserted, total, true, err) //nolint:errcheck
			if err != nil {
				log.Printf("ERROR: scheduled sync failed: %v", err)
			} else {
				log.Printf("Scheduled sync complete: %d/%d upserted", upserted, total)
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Session store (in-memory)
// ---------------------------------------------------------------------------

type session struct {
	AccessToken  string
	RefreshToken string
	APIBase      string
	CreatedAt    time.Time
}

type sessionStore struct {
	secret string
	store  map[string]*session
}

func newSessionStore(secret string) *sessionStore {
	return &sessionStore{secret: secret, store: map[string]*session{}}
}

func (s *sessionStore) create(sess *session) string {
	id := randomState()
	s.store[id] = sess
	return id
}

func (s *sessionStore) get(id string) (*session, bool) {
	sess, ok := s.store[id]
	return sess, ok
}

func (s *sessionStore) delete(id string) {
	delete(s.store, id)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func randomState() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	return base64.URLEncoding.EncodeToString(b)
}
