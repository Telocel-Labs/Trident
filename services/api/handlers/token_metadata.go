package handlers

import (
	"errors"
	"net/http"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/validation"
	"github.com/jackc/pgx/v5"
)

// TokenMetadataResponse is the JSON body for GET /v1/contracts/{id}/metadata.
//
// Name/Symbol/Decimals/ResolvedAt are null whenever IsToken is false — either
// the contract has never been resolved yet (no token event observed for it),
// or it was resolved and does not implement the SEP-41 read interface. Both
// cases are indistinguishable from this endpoint alone, matching the
// resolver's own cached negative result (issue #263).
type TokenMetadataResponse struct {
	ContractID string  `json:"contract_id"`
	Network    string  `json:"network"`
	IsToken    bool    `json:"is_token"`
	Name       *string `json:"name"`
	Symbol     *string `json:"symbol"`
	Decimals   *int32  `json:"decimals"`
	ResolvedAt *string `json:"resolved_at"`
}

// TokenMetadata handles GET /v1/contracts/{id}/metadata (issue #263).
//
// Reads the token_metadata table, populated and refreshed by the Rust
// indexer's simulateTransaction-based resolver — this handler never calls
// the Stellar RPC itself. A contract with no row yet (not resolved) or a
// cached non-token result both return 200 with is_token: false rather than
// a 404, consistent with ContractEventSchemas's "always 200" convention.
func TokenMetadata(db DBPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.PathValue("id")
		if verr := validation.ValidateRequiredContractID("id", contractID); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}
		if db == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.UNAVAILABLE, "database unavailable")
			return
		}

		network := middleware.NetworkFromContext(r.Context())

		var (
			isToken    bool
			name       *string
			symbol     *string
			decimals   *int32
			resolvedAt *string
		)
		err := db.QueryRow(r.Context(), `
			SELECT is_token, name, symbol, decimals, resolved_at::text
			FROM token_metadata
			WHERE contract_id = $1 AND network = $2
		`, contractID, network).Scan(&isToken, &name, &symbol, &decimals, &resolvedAt)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load token metadata")
			return
		}

		writeJSON(w, http.StatusOK, TokenMetadataResponse{
			ContractID: contractID,
			Network:    network,
			IsToken:    isToken,
			Name:       name,
			Symbol:     symbol,
			Decimals:   decimals,
			ResolvedAt: resolvedAt,
		})
	}
}
