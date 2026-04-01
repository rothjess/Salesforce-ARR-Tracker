package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	dbpkg "github.com/coder/arr-tracker-sf/internal/db"
	"github.com/coder/arr-tracker-sf/internal/salesforce"
)

// Handler wires together DB and Salesforce.
type Handler struct {
	db *dbpkg.DB
	sf *salesforce.Client
}

func New(db *dbpkg.DB, sf *salesforce.Client) *Handler {
	return &Handler{db: db, sf: sf}
}

// RegisterRoutes attaches all HTTP routes to mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", h.health)
	mux.HandleFunc("/api/summary", h.summary)
	mux.HandleFunc("/api/contracts", h.contracts)
	mux.HandleFunc("/api/sync", h.sync)

	// Serve React frontend (embedded or from /web/dist)
	mux.Handle("/", http.FileServer(http.Dir("./web/dist")))
}

// ---------------------------------------------------------------------------
// Handlers
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
	stageFilter := r.URL.Query().Get("stage") // ALL | CLOSED_WON (default)
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

	full := r.URL.Query().Get("full") == "true"
	upserted, total, err := h.runSync(!full)
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

// runSync fetches from Salesforce and upserts to DB.
// incremental=true means fetch only records modified since last sync.
func (h *Handler) runSync(incremental bool) (upserted, total int, err error) {
	var since time.Time
	if incremental {
		since, err = h.db.LastSyncTime()
		if err != nil {
			log.Printf("WARN: could not get last sync time, doing full sync: %v", err)
			since = time.Time{}
		}
	}

	log.Printf("Starting sync (incremental=%v, since=%v)", incremental, since)

	sfContracts, err := h.sf.FetchOpportunities(since)
	if err != nil {
		return 0, 0, err
	}

	total = len(sfContracts)
	log.Printf("Fetched %d opportunities from Salesforce", total)

	// Convert salesforce.Contract → db.Contract
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

// ---------------------------------------------------------------------------
// Background scheduler
// ---------------------------------------------------------------------------

func (h *Handler) StartScheduler() {
	go func() {
		// Initial sync on startup
		upserted, total, err := h.runSync(false)
		h.db.LogSync(upserted, total, false, err) //nolint:errcheck
		if err != nil {
			log.Printf("ERROR: initial sync failed: %v", err)
		} else {
			log.Printf("Initial sync complete: %d/%d upserted", upserted, total)
		}

		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			upserted, total, err := h.runSync(true)
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
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
