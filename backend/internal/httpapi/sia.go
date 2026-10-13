package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/siaindexd"
	"virtroid/backend/internal/store"
)

// NewSiaRegistry is the Sia indexer the configuration names, or nil when Sia
// storage is off.
func NewSiaRegistry(cfg config.ServerConfig) (*siaindexd.Registry, error) {
	if strings.TrimSpace(cfg.SiaIndexerAppURL) == "" {
		return nil, nil
	}
	return siaindexd.New(siaindexd.Config{
		AppURL:          cfg.SiaIndexerAppURL,
		AdminURL:        cfg.SiaIndexerAdminURL,
		AdminPassword:   cfg.SiaIndexerAdminPassword,
		KeySecret:       []byte(cfg.SiaKeySecret),
		Quota:           cfg.SiaQuota,
		MaxPinnedBytes:  nonNegativeBytes(cfg.SiaMaxPinnedBytes),
		FundTargetBytes: nonNegativeBytes(cfg.SiaFundTargetBytes),
		DataShards:      cfg.SiaDataShards,
		TotalShards:     cfg.SiaTotalShards,
	}, nil)
}

func nonNegativeBytes(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}

// runtimeSiaCredentials gives the node holding a runtime the Sia indexer
// credentials of the runtime's account. A deleted account gets none: the
// control plane removes it from the indexer itself.
func (a *API) runtimeSiaCredentials(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}
	if a.sia == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "sia_not_configured"})
		return
	}
	accountID, deleted, err := a.store.SiaCredentialAccount(r.Context(), r.PathValue("id"), node.id)
	if errors.Is(err, store.ErrRuntimeNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "runtime is not assigned to this node"})
		return
	}
	if err != nil {
		writeInternalAPIError(w, "sia_credentials_lookup_failed", err)
		return
	}
	if deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "sia_not_configured", "reason": "account deleted"})
		return
	}
	// Recorded before registering, so a deletion racing the registration
	// still removes the account from the indexer.
	if err := a.store.MarkAccountSiaRegistered(r.Context(), accountID); err != nil {
		writeInternalAPIError(w, "sia_registration_record_failed", err)
		return
	}
	credentials, err := a.sia.Credentials(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "sia_registration_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, credentials)
}
