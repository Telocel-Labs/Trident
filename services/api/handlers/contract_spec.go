package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/validation"
	"github.com/jackc/pgx/v5"
)

// contractSpecQueryTimeout bounds the DB call in ContractSpec so a runaway
// query can't hold a pool connection for the request's full budget (issue
// #238).
const contractSpecQueryTimeout = 5 * time.Second

// ContractSpecFunction is one function captured from a contract's parsed
// spec (issue #260).
type ContractSpecFunction struct {
	Name string `json:"name"`
}

// ContractSpecResponse reports a tracked contract's parsed spec and the
// standard interfaces detected from it (issues #260, #269).
type ContractSpecResponse struct {
	ContractID   string                 `json:"contract_id"`
	Network      string                 `json:"network"`
	CodeHash     string                 `json:"code_hash"`
	HasSpec      bool                   `json:"has_spec"`
	ContractType string                 `json:"contract_type"`
	Interfaces   []string               `json:"interfaces"`
	Functions    []ContractSpecFunction `json:"functions"`
}

// ContractSpec exposes a tracked contract's spec-derived metadata: its
// parsed functions and the standard interfaces detected from them (issues
// #260, #269). Returns 404 when the indexer has not yet synced a spec for
// this contract (not tracked, or not yet fetched).
func ContractSpec(db SchemaRegistryDB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.PathValue("id")
		if verr := validation.ValidateRequiredContractID("id", contractID); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}
		if db == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "contract spec store unavailable")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), contractSpecQueryTimeout)
		defer cancel()

		network := middleware.NetworkFromContext(r.Context())
		resp, err := loadContractSpec(ctx, db, contractID, network)
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, httputil.NOT_FOUND, "no spec recorded for this contract")
			return
		}
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.INTERNAL, "failed to load contract spec")
			return
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

func loadContractSpec(ctx context.Context, db SchemaRegistryDB, contractID, network string) (ContractSpecResponse, error) {
	var (
		codeHash     string
		hasSpec      bool
		contractType string
		functionsRaw []byte
		interfaceRaw []byte
	)

	err := db.QueryRow(ctx, `
        SELECT code_hash, has_spec, contract_type, functions, interfaces
        FROM contract_specs
        WHERE contract_id = $1 AND network = $2
    `, contractID, network).Scan(&codeHash, &hasSpec, &contractType, &functionsRaw, &interfaceRaw)
	if err != nil {
		return ContractSpecResponse{}, err
	}

	var functions []ContractSpecFunction
	if err := json.Unmarshal(functionsRaw, &functions); err != nil {
		return ContractSpecResponse{}, err
	}
	var interfaces []string
	if err := json.Unmarshal(interfaceRaw, &interfaces); err != nil {
		return ContractSpecResponse{}, err
	}
	if functions == nil {
		functions = []ContractSpecFunction{}
	}
	if interfaces == nil {
		interfaces = []string{}
	}

	return ContractSpecResponse{
		ContractID:   contractID,
		Network:      network,
		CodeHash:     codeHash,
		HasSpec:      hasSpec,
		ContractType: contractType,
		Interfaces:   interfaces,
		Functions:    functions,
	}, nil
}
