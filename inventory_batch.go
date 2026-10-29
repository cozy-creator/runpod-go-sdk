package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type DataCentersResult struct {
	DataCenters []DataCenter
	Err         error
}
type GPUOffersResult struct {
	Offers []GPUOffer
	Err    error
}
type CPUOfferResult struct {
	Offer CPUOffer
	Err   error
}
type InventoryRequest struct {
	Availability   []GPUAvailabilityFilter
	GPUOffers      []GPUOfferFilter
	CPUOffers      []CPUOfferRequest
	NetworkVolumes bool
}
type InventoryResult struct {
	DataCenters       []DataCentersResult
	GPUOffers         []GPUOffersResult
	CPUOffers         []CPUOfferResult
	NetworkVolumes    []NetworkVolume
	NetworkVolumesErr error
}

// ObserveInventory performs scoped REST v2 reads sequentially, one attempt per
// distinct URL per pass. Identical reads share exact bytes within the pass.
// Results preserve input order and partial failures. A rate limit or canceled
// context prevents further HTTP; the caller owns refresh cadence and retries.
func (c *Client) ObserveInventory(ctx context.Context, request InventoryRequest) (InventoryResult, error) {
	for i := range request.Availability {
		if err := validateGPUAvailabilityFilter(&request.Availability[i]); err != nil {
			return InventoryResult{}, err
		}
	}
	for i := range request.GPUOffers {
		if err := validateGPUOfferFilter(&request.GPUOffers[i]); err != nil {
			return InventoryResult{}, err
		}
	}
	for _, r := range request.CPUOffers {
		if err := validateCPUOfferRequest(r); err != nil {
			return InventoryResult{}, err
		}
	}
	result := InventoryResult{DataCenters: make([]DataCentersResult, len(request.Availability)), GPUOffers: make([]GPUOffersResult, len(request.GPUOffers)), CPUOffers: make([]CPUOfferResult, len(request.CPUOffers))}
	type cachedRead struct {
		raw json.RawMessage
		err error
	}
	cache := map[string]cachedRead{}
	var stop error
	read := func(ctx context.Context, endpoint string, target any) error {
		if stop != nil {
			return stop
		}
		if err := ctx.Err(); err != nil {
			stop = err
			return err
		}
		cached, ok := cache[endpoint]
		if !ok {
			response, err := c.makeRequestBytesWithRetryLimit(ctx, http.MethodGet, endpoint, nil, nil, 0)
			cached.err = err
			if err == nil {
				cached.err = c.handleResponse(response, &cached.raw)
			}
			cache[endpoint] = cached
		}
		if cached.err != nil {
			if errors.Is(cached.err, ErrRateLimited) || ctx.Err() != nil {
				stop = cached.err
			}
			return cached.err
		}
		if err := json.Unmarshal(cached.raw, target); err != nil {
			return fmt.Errorf("decode inventory %s: %w", endpoint, err)
		}
		return nil
	}
	successes, failures := 0, 0
	var first error
	record := func(err error) {
		if err == nil {
			successes++
			return
		}
		failures++
		if first == nil {
			first = err
		}
		if errors.Is(err, ErrRateLimited) || ctx.Err() != nil {
			stop = err
		}
	}
	for i := range request.Availability {
		result.DataCenters[i].DataCenters, result.DataCenters[i].Err = c.listDataCenters(ctx, &request.Availability[i], read)
		record(result.DataCenters[i].Err)
	}
	if request.NetworkVolumes {
		result.NetworkVolumes, result.NetworkVolumesErr = c.listNetworkVolumes(ctx, read)
		record(result.NetworkVolumesErr)
	}
	for i := range request.GPUOffers {
		result.GPUOffers[i].Offers, result.GPUOffers[i].Err = c.listGPUOffers(ctx, &request.GPUOffers[i], read)
		record(result.GPUOffers[i].Err)
	}
	for i, r := range request.CPUOffers {
		result.CPUOffers[i].Offer, result.CPUOffers[i].Err = c.getCPUOffer(ctx, r, read)
		record(result.CPUOffers[i].Err)
	}
	if failures > 0 && successes == 0 {
		return result, first
	}
	return result, nil
}
func (c *Client) ListDataCentersBatch(ctx context.Context, f []GPUAvailabilityFilter) ([]DataCentersResult, error) {
	r, e := c.ObserveInventory(ctx, InventoryRequest{Availability: f})
	return r.DataCenters, e
}
func (c *Client) ListGPUOffersBatch(ctx context.Context, f []GPUOfferFilter) ([]GPUOffersResult, error) {
	r, e := c.ObserveInventory(ctx, InventoryRequest{GPUOffers: f})
	return r.GPUOffers, e
}
func (c *Client) GetCPUOffersBatch(ctx context.Context, r []CPUOfferRequest) ([]CPUOfferResult, error) {
	out, e := c.ObserveInventory(ctx, InventoryRequest{CPUOffers: r})
	return out.CPUOffers, e
}
