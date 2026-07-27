package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/assetcatalog"
)

func (h handler) getMarketAssetSearch(w http.ResponseWriter, r *http.Request) {
	if !h.requireAssetSession(w, r) {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 25 {
			writeError(
				w,
				http.StatusBadRequest,
				errors.New("limit must be between 1 and 25"),
			)
			return
		}
		limit = value
	}
	result, err := h.deps.Assets.Search(
		r.Context(), r.URL.Query().Get("q"), limit,
	)
	if err != nil {
		writeAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getMarketAssetResolve(w http.ResponseWriter, r *http.Request) {
	if !h.requireAssetSession(w, r) {
		return
	}
	candidate, err := h.deps.Assets.ResolveContract(
		r.Context(),
		r.URL.Query().Get("chain"),
		r.URL.Query().Get("contract"),
	)
	if err != nil {
		writeAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": candidate})
}

func (h handler) postMarketAssetManualResolve(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !h.requireAssetSession(w, r) {
		return
	}
	var input assetcatalog.ManualResolveInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Assets.ResolveManualContracts(r.Context(), input)
	if err != nil {
		writeAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postMarketAssetVerifyCrossChain(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !h.requireAssetSession(w, r) {
		return
	}
	var input assetcatalog.CrossChainVerifyInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Assets.VerifyCrossChainIdentity(r.Context(), input)
	if err != nil {
		writeAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getMarketAssetLogo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAssetSession(w, r) {
		return
	}
	logo, err := h.deps.Assets.Logo(
		r.Context(), r.URL.Query().Get("source"),
	)
	if err != nil {
		writeAssetError(w, err)
		return
	}
	w.Header().Set("ETag", logo.ETag)
	if r.Header.Get("If-None-Match") == logo.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", logo.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(logo.Body)
}

func (h handler) requireAssetSession(
	w http.ResponseWriter,
	r *http.Request,
) bool {
	_, ok := h.requireSession(w, r)
	if !ok {
		return false
	}
	if h.deps.Assets == nil {
		serviceUnavailable(w)
		return false
	}
	return true
}

func writeAssetError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	publicErr := error(assetcatalog.ErrUnavailable)
	switch {
	case errors.Is(err, assetcatalog.ErrNotFound):
		status = http.StatusNotFound
		publicErr = assetcatalog.ErrNotFound
	case errors.Is(err, assetcatalog.ErrUnavailable):
		// Keep the normalized public error set above.
	case errors.Is(err, assetcatalog.ErrInvalidLogo):
		status = http.StatusBadRequest
		publicErr = assetcatalog.ErrInvalidLogo
	case errors.Is(err, assetcatalog.ErrInvalidQuery):
		status = http.StatusBadRequest
		publicErr = assetcatalog.ErrInvalidQuery
	case errors.Is(err, assetcatalog.ErrInvalidManualRequest):
		status = http.StatusBadRequest
		publicErr = assetcatalog.ErrInvalidManualRequest
	case errors.Is(err, assetcatalog.ErrContractUnreadable):
		status = http.StatusUnprocessableEntity
		publicErr = assetcatalog.ErrContractUnreadable
	case errors.Is(err, assetcatalog.ErrInvalidCrossChainRequest):
		status = http.StatusBadRequest
		publicErr = assetcatalog.ErrInvalidCrossChainRequest
	case errors.Is(err, assetcatalog.ErrCrossChainIdentityUnverified):
		status = http.StatusUnprocessableEntity
		publicErr = assetcatalog.ErrCrossChainIdentityUnverified
	case errors.Is(err, assetcatalog.ErrCrossChainIdentityConflict):
		status = http.StatusConflict
		publicErr = assetcatalog.ErrCrossChainIdentityConflict
	}
	writeError(w, status, publicErr)
}
