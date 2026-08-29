package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/validation"
)

// contractStorageQueryTimeout bounds the DB calls in ContractStorageLatest/
// ContractStorageHistory so a runaway query can't hold a pool connection for
// the request's full budget (issue #238).
const contractStorageQueryTimeout = 5 * time.Second

// ContractStorageValue is one contract-storage key's value at a given ledger
// (issue #270).
type ContractStorageValue struct {
	StorageKey     string          `json:"storage_key"`
	Key            json.RawMessage `json:"key"`
	Value          json.RawMessage `json:"value"`
	LedgerSequence int64           `json:"ledger_sequence"`
	ObservedAt     time.Time       `json:"observed_at"`
}

// ContractStorageResponse lists the latest known value for every storage key
// snapshotted for a tracked contract (issue #270).
type ContractStorageResponse struct {
	ContractID string                 `json:"contract_id"`
	Network    string                 `json:"network"`
	Values     []ContractStorageValue `json:"values"`
}

// ContractStorageLatest returns the most recently observed value for every
// storage key snapshotted for a tracked contract (issue #270).
func ContractStorageLatest(db SchemaRegistryDB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.PathValue("id")
		if verr := validation.ValidateRequiredContractID("id", contractID); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}
		if db == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "contract storage store unavailable")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), contractStorageQueryTimeout)
		defer cancel()

		network := middleware.NetworkFromContext(r.Context())
		rows, err := db.Query(ctx, `
            SELECT DISTINCT ON (storage_key)
                storage_key, key_json, value_json, ledger_sequence, created_at
            FROM contract_storage_snapshots
            WHERE contract_id = $1 AND network = $2
            ORDER BY storage_key, ledger_sequence DESC
        `, contractID, network)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage")
			return
		}
		defer rows.Close()

		values := []ContractStorageValue{}
		for rows.Next() {
			var v ContractStorageValue
			var keyRaw, valueRaw []byte
			if err := rows.Scan(&v.StorageKey, &keyRaw, &valueRaw, &v.LedgerSequence, &v.ObservedAt); err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage")
				return
			}
			v.Key = json.RawMessage(keyRaw)
			if valueRaw != nil {
				v.Value = json.RawMessage(valueRaw)
			}
			values = append(values, v)
		}
		if err := rows.Err(); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage")
			return
		}

		writeJSON(w, http.StatusOK, ContractStorageResponse{
			ContractID: contractID,
			Network:    network,
			Values:     values,
		})
	}
}

// ContractStorageHistory returns every recorded change for a single storage
// key, oldest first, for a tracked contract (issue #270).
func ContractStorageHistory(db SchemaRegistryDB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.PathValue("id")
		if verr := validation.ValidateRequiredContractID("id", contractID); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}
		storageKey := r.URL.Query().Get("key")
		if storageKey == "" {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "key query parameter is required")
			return
		}
		if db == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "contract storage store unavailable")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), contractStorageQueryTimeout)
		defer cancel()

		network := middleware.NetworkFromContext(r.Context())
		rows, err := db.Query(ctx, `
            SELECT storage_key, key_json, value_json, ledger_sequence, created_at
            FROM contract_storage_snapshots
            WHERE contract_id = $1 AND network = $2 AND storage_key = $3
            ORDER BY ledger_sequence ASC
        `, contractID, network, storageKey)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage history")
			return
		}
		defer rows.Close()

		values := []ContractStorageValue{}
		for rows.Next() {
			var v ContractStorageValue
			var keyRaw, valueRaw []byte
			if err := rows.Scan(&v.StorageKey, &keyRaw, &valueRaw, &v.LedgerSequence, &v.ObservedAt); err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage history")
				return
			}
			v.Key = json.RawMessage(keyRaw)
			if valueRaw != nil {
				v.Value = json.RawMessage(valueRaw)
			}
			values = append(values, v)
		}
		if err := rows.Err(); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract storage history")
			return
		}

		writeJSON(w, http.StatusOK, ContractStorageResponse{
			ContractID: contractID,
			Network:    network,
			Values:     values,
		})
	}
}
