/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Credentials (stored in systemconfig.json)

// Credentials holds the Netatmo OAuth2 application credentials.
// No username or password — authentication uses the OAuth2 Authorization Code flow.
// On first run the system prints an authorization URL; after the user clicks it in a
// browser the resulting tokens are saved to tokens.json and reused on every subsequent run.
type Credentials struct {
	ClientID     string `json:"clientID"`
	ClientSecret string `json:"clientSecret"`
	StationName  string `json:"stationName"` // leave empty to use the first station found
	Period       int    `json:"period"`       // polling interval in seconds; default 300
}

const (
	tokenFile        = "tokens.json"
	oauthCallbackPort = "9999"
	oauthRedirectURI  = "http://localhost:9999/callback"
)

// savedTokens is the structure written to / read from tokens.json.
type savedTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func loadTokenFile() (*savedTokens, error) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	var t savedTokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func saveTokenFile(t savedTokens) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tokenFile, data, 0600)
}

// -------------------------------------Token management

// TokenManager handles Netatmo OAuth2 authentication.
type TokenManager struct {
	Credentials
	accessToken  string
	refreshToken string
	mu           sync.Mutex
}

// newTokenManager parses credentials from config and ensures a valid token is available.
// It loads tokens.json if it exists and tries to refresh; otherwise it starts the
// one-time browser authorization flow.
func newTokenManager(uac usecases.ConfigurableAsset) (*TokenManager, error) {
	if len(uac.Traits) == 0 {
		return nil, fmt.Errorf("no credentials found in configuration")
	}
	var creds Credentials
	if err := json.Unmarshal(uac.Traits[0], &creds); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	if creds.Period == 0 {
		creds.Period = 300
	}
	tm := &TokenManager{Credentials: creds}

	// Try to reuse a saved refresh token first.
	if saved, err := loadTokenFile(); err == nil && saved.RefreshToken != "" {
		tm.accessToken = saved.AccessToken
		tm.refreshToken = saved.RefreshToken
		if err := tm.refresh(); err == nil {
			log.Println("Netatmo: resumed session from tokens.json")
			return tm, nil
		}
		log.Println("Netatmo: saved token expired, re-authorizing...")
	}

	// No valid saved token — run the one-time browser flow.
	if err := tm.authorizeWithBrowser(); err != nil {
		return nil, fmt.Errorf("Netatmo authorization failed: %w", err)
	}
	return tm, nil
}

// authorizeWithBrowser starts a local callback server, prints the Netatmo authorization
// URL for the user to open in a browser, waits for the redirect, exchanges the code for
// tokens, and saves them to tokens.json.
func (tm *TokenManager) authorizeWithBrowser() error {
	authURL := "https://api.netatmo.com/oauth2/authorize?" + url.Values{
		"client_id":     {tm.ClientID},
		"redirect_uri":  {oauthRedirectURI},
		"scope":         {"read_station"},
		"response_type": {"code"},
	}.Encode()

	fmt.Println("\n--- Netatmo Authorization Required ---")
	fmt.Println("Open this URL in your browser and log in:")
	fmt.Println(authURL)
	fmt.Println("--------------------------------------")

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	srv := &http.Server{Addr: ":" + oauthCallbackPort, Handler: mux}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback: %s", r.URL.RawQuery)
			fmt.Fprintln(w, "Authorization failed — no code received.")
			return
		}
		fmt.Fprintln(w, "Authorization successful — you can close this tab.")
		codeCh <- code
	})

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		srv.Close()
		return err
	case <-time.After(5 * time.Minute):
		srv.Close()
		return fmt.Errorf("timed out waiting for browser authorization (5 min)")
	}
	srv.Close()

	return tm.exchangeCode(code)
}

// exchangeCode exchanges an authorization code for access and refresh tokens.
func (tm *TokenManager) exchangeCode(code string) error {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", tm.ClientID)
	form.Set("client_secret", tm.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", oauthRedirectURI)

	return tm.postToken(form)
}

// refresh exchanges the current refresh token for a new access/refresh token pair.
func (tm *TokenManager) refresh() error {
	tm.mu.Lock()
	rt := tm.refreshToken
	tm.mu.Unlock()

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)
	form.Set("client_id", tm.ClientID)
	form.Set("client_secret", tm.ClientSecret)

	return tm.postToken(form)
}

// postToken posts a token request and stores the resulting tokens in memory and on disk.
func (tm *TokenManager) postToken(form url.Values) error {
	resp, err := http.PostForm("https://api.netatmo.com/oauth2/token", form)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("token read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token request HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("token decode: %w", err)
	}
	if result.Error != "" {
		return fmt.Errorf("netatmo token error: %s", result.Error)
	}

	tm.mu.Lock()
	tm.accessToken = result.AccessToken
	tm.refreshToken = result.RefreshToken
	tm.mu.Unlock()

	if err := saveTokenFile(savedTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
	}); err != nil {
		log.Printf("Netatmo: warning — could not save tokens.json: %v\n", err)
	}
	return nil
}

// getToken returns the current access token.
func (tm *TokenManager) getToken() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.accessToken
}

// getWithAutoRefresh performs a Bearer-authenticated GET.
// On 401 it refreshes; if refresh fails it triggers a new browser authorization.
func (tm *TokenManager) getWithAutoRefresh(rawURL string) ([]byte, error) {
	doGET := func(token string) (int, []byte, error) {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, body, err
	}

	status, body, err := doGET(tm.getToken())
	if err != nil {
		return nil, err
	}
	if status == 401 {
		log.Println("Netatmo: access token expired, refreshing...")
		if rerr := tm.refresh(); rerr != nil {
			log.Printf("Netatmo: refresh failed (%v), re-authorizing via browser...", rerr)
			if aerr := tm.authorizeWithBrowser(); aerr != nil {
				return nil, fmt.Errorf("re-authorization failed: %w", aerr)
			}
		}
		_, body, err = doGET(tm.getToken())
		return body, err
	}
	return body, nil
}

// fetchStationData calls the Netatmo getstationsdata endpoint and returns parsed results.
func (tm *TokenManager) fetchStationData() (*StationsDataResponse, error) {
	body, err := tm.getWithAutoRefresh("https://api.netatmo.com/api/getstationsdata")
	if err != nil {
		return nil, err
	}
	var resp StationsDataResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal station data: %w\nresponse: %s", err, string(body))
	}
	return &resp, nil
}

// -------------------------------------Measurement cache

// CachedMeasurement holds one measurement value and its timestamp.
type CachedMeasurement struct {
	Value     float64
	Timestamp time.Time
}

// ModuleCache is a thread-safe store of measurements keyed by asset name and service subpath.
type ModuleCache struct {
	mu   sync.RWMutex
	data map[string]map[string]CachedMeasurement // assetName → subPath → value
}

func newModuleCache() *ModuleCache {
	return &ModuleCache{data: make(map[string]map[string]CachedMeasurement)}
}

func (c *ModuleCache) update(assetName string, measurements map[string]float64, ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data[assetName] == nil {
		c.data[assetName] = make(map[string]CachedMeasurement)
	}
	for k, v := range measurements {
		c.data[assetName][k] = CachedMeasurement{Value: v, Timestamp: ts}
	}
}

func (c *ModuleCache) get(assetName, subPath string) *CachedMeasurement {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.data[assetName]; ok {
		if v, ok := m[subPath]; ok {
			return &v
		}
	}
	return nil
}

// -------------------------------------Unit asset Traits

// Traits is the runtime state for a module unit asset.
type Traits struct {
	assetName string
	cache     *ModuleCache
}

// -------------------------------------Template

// initTemplate returns the template unit asset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	return &components.UnitAsset{
		Name:        "MeteoStation",
		Mission:     "provide_weather_data",
		Details:     map[string][]string{},
		ServicesMap: components.Services{},
		Traits: &Credentials{
			ClientID:     "your_netatmo_client_id",
			ClientSecret: "your_netatmo_client_secret",
			StationName:  "",
			Period:       300,
		},
	}
}

// -------------------------------------Asset instantiation entry point

// newResources is the single entry point called by main for this system.
// It authenticates with the Netatmo API, discovers all modules on the configured station,
// builds one UnitAsset per module, starts the background poller, and returns the assets.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	tm, err := newTokenManager(uac)
	if err != nil {
		log.Fatalf("Netatmo authentication failed: %v\n", err)
	}

	stationData, err := tm.fetchStationData()
	if err != nil {
		log.Fatalf("could not fetch Netatmo station data: %v\n", err)
	}
	if len(stationData.Body.Devices) == 0 {
		log.Fatal("no Netatmo stations found for this account")
	}

	cache := newModuleCache()
	assets := buildAssets(stationData, tm.StationName, sys, cache)
	if len(assets) == 0 {
		log.Fatal("no modules found — check stationName filter in systemconfig.json")
	}
	for _, ua := range assets {
		log.Printf("registered asset %q\n", ua.GetName())
	}

	go pollNetatmo(sys.Ctx, tm, cache)

	return assets, func() {
		log.Println("disconnecting from Netatmo")
	}
}

// -------------------------------------Dynamic asset construction

// buildAssets creates one UnitAsset per module and pre-populates the cache.
func buildAssets(resp *StationsDataResponse, stationFilter string, sys *components.System, cache *ModuleCache) []*components.UnitAsset {
	var assets []*components.UnitAsset

	for _, device := range resp.Body.Devices {
		if stationFilter != "" && device.StationName != stationFilter {
			continue
		}
		stationName := device.StationName

		if info, ok := moduleTypeMap["NAMain"]; ok {
			ts := time.Unix(device.DashboardData.TimeUTC, 0)
			cache.update(info.assetName, extractMeasurements("NAMain", device.DashboardData), ts)
			assets = append(assets, newModuleAsset(info, device.ModuleName, stationName, sys, cache))
		}

		for _, mod := range device.Modules {
			info, ok := moduleTypeMap[mod.Type]
			if !ok {
				log.Printf("meteorologue: unknown module type %q — skipping\n", mod.Type)
				continue
			}
			ts := time.Unix(mod.DashboardData.TimeUTC, 0)
			cache.update(info.assetName, extractMeasurements(mod.Type, mod.DashboardData), ts)
			assets = append(assets, newModuleAsset(info, mod.ModuleName, stationName, sys, cache))
		}

		break // only process the first matching station
	}

	return assets
}

// newModuleAsset creates a UnitAsset for one Netatmo module.
func newModuleAsset(info moduleInfo, moduleName, stationName string, sys *components.System, cache *ModuleCache) *components.UnitAsset {
	t := &Traits{assetName: info.assetName, cache: cache}

	services := make(components.Services)
	for _, spec := range info.services {
		s := &components.Service{
			Definition:  spec.definition,
			SubPath:     spec.subPath,
			Details:     map[string][]string{"Unit": {spec.unit}, "Forms": {"SignalA_v1a"}},
			RegPeriod:   30,
			Description: spec.description,
		}
		services[spec.subPath] = s
	}

	ua := &components.UnitAsset{
		Name:    info.assetName,
		Mission: "provide_weather_data",
		Owner:   sys,
		Details: map[string][]string{
			"FunctionalLocation": {stationName},
			"ModuleName":         {moduleName},
		},
		ServicesMap: services,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// -------------------------------------Background poller

// pollNetatmo refreshes the measurement cache on every tick until the context is cancelled.
func pollNetatmo(ctx context.Context, tm *TokenManager, cache *ModuleCache) {
	ticker := time.NewTicker(time.Duration(tm.Period) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := tm.fetchStationData()
			if err != nil {
				log.Printf("Netatmo poll error: %v\n", err)
				continue
			}
			for _, device := range resp.Body.Devices {
				if tm.StationName != "" && device.StationName != tm.StationName {
					continue
				}
				if info, ok := moduleTypeMap["NAMain"]; ok {
					ts := time.Unix(device.DashboardData.TimeUTC, 0)
					cache.update(info.assetName, extractMeasurements("NAMain", device.DashboardData), ts)
				}
				for _, mod := range device.Modules {
					if info, ok := moduleTypeMap[mod.Type]; ok {
						ts := time.Unix(mod.DashboardData.TimeUTC, 0)
						cache.update(info.assetName, extractMeasurements(mod.Type, mod.DashboardData), ts)
					}
				}
				break
			}
			log.Println("Netatmo: data refreshed")
		}
	}
}
