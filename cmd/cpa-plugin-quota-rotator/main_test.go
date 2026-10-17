package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestStatusManagementRouteReturnsJSON(t *testing.T) {
	for _, path := range []string{"/v0/management/plugins/quota-rotator/status", "/plugins/quota-rotator/status"} {
		t.Run(path, func(t *testing.T) {
			req, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: path})
			raw, err := handleManagement(req)
			if err != nil {
				t.Fatal(err)
			}
			var env envelope
			if err = json.Unmarshal(raw, &env); err != nil || !env.OK {
				t.Fatalf("invalid envelope: %s", raw)
			}
			var response pluginapi.ManagementResponse
			if err = json.Unmarshal(env.Result, &response); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(response.Headers.Get("Content-Type"), "application/json") || !json.Valid(response.Body) {
				t.Fatalf("status route returned HTML: %s", response.Headers.Get("Content-Type"))
			}
		})
	}
}

func TestStatusResourceRouteReturnsHTML(t *testing.T) {
	for _, path := range []string{"/v0/resource/plugins/quota-rotator/status", "/status"} {
		t.Run(path, func(t *testing.T) {
			req, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: path})
			raw, err := handleManagement(req)
			if err != nil {
				t.Fatal(err)
			}
			var env envelope
			if err = json.Unmarshal(raw, &env); err != nil || !env.OK {
				t.Fatalf("invalid envelope: %s", raw)
			}
			var response pluginapi.ManagementResponse
			if err = json.Unmarshal(env.Result, &response); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(response.Headers.Get("Content-Type"), "text/html") || !strings.Contains(string(response.Body), "Smart Quota Rotator v1.2.4") {
				t.Fatal("resource page missing")
			}
		})
	}
}
