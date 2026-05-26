package main

import (
	"net/http"
	"testing"
)

func TestRouteRequest_WriteGoesToHomeRegion(t *testing.T) {
	tr := TenantRegion{
		TenantID:      "tenant-a",
		HomeRegion:    "us-east-1",
		DataResidency: ResidencyUS,
	}
	localRegion := "eu-west-1"

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequest(method, "/v1/policies", nil)
		target, reason := routeRequest(tr, localRegion, req)
		if target != "us-east-1" {
			t.Errorf("method %s: expected us-east-1, got %s", method, target)
		}
		if reason != "write_affinity" {
			t.Errorf("method %s: expected write_affinity, got %s", method, reason)
		}
	}
}

func TestRouteRequest_ReadGoesToLocal(t *testing.T) {
	tr := TenantRegion{
		TenantID:      "tenant-b",
		HomeRegion:    "us-east-1",
		DataResidency: ResidencyUS,
	}
	localRegion := "eu-west-1"

	req, _ := http.NewRequest(http.MethodGet, "/v1/audit", nil)
	target, reason := routeRequest(tr, localRegion, req)
	if target != "eu-west-1" {
		t.Errorf("expected local read to eu-west-1, got %s", target)
	}
	if reason != "local_read" {
		t.Errorf("expected local_read, got %s", reason)
	}
}

func TestRouteRequest_EUResidencyEnforced(t *testing.T) {
	tr := TenantRegion{
		TenantID:      "tenant-eu",
		HomeRegion:    "eu-west-1",
		DataResidency: ResidencyEU,
	}
	localRegion := "us-east-1"

	req, _ := http.NewRequest(http.MethodGet, "/v1/data", nil)
	target, reason := routeRequest(tr, localRegion, req)
	if target != "eu-west-1" {
		t.Errorf("EU residency: expected eu-west-1, got %s", target)
	}
	if reason != "data_residency" {
		t.Errorf("expected data_residency, got %s", reason)
	}
}

func TestRouteRequest_MultiResidencyFollowsLocal(t *testing.T) {
	tr := TenantRegion{
		TenantID:      "tenant-multi",
		HomeRegion:    "us-east-1",
		DataResidency: ResidencyMulti,
	}
	localRegion := "eu-west-1"

	req, _ := http.NewRequest(http.MethodGet, "/v1/data", nil)
	target, reason := routeRequest(tr, localRegion, req)
	if target != "eu-west-1" {
		t.Errorf("multi residency read: expected local eu-west-1, got %s", target)
	}
	if reason != "local_read" {
		t.Errorf("expected local_read, got %s", reason)
	}
}

func TestTTLCache_SetGet(t *testing.T) {
	cache := newTTLCache[string, int](100)
	cache.Set("key", 42)
	v, ok := cache.Get("key")
	if !ok || v != 42 {
		t.Errorf("expected (42, true), got (%d, %v)", v, ok)
	}
}

func TestTTLCache_Miss(t *testing.T) {
	cache := newTTLCache[string, int](100)
	_, ok := cache.Get("missing")
	if ok {
		t.Error("expected cache miss, got hit")
	}
}
