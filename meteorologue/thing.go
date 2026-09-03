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
	"strings"
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
	Period       int    `json:"period"`      // polling interval in seconds; default 300
}

const (
	tokenFile         = "tokens.json"
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
	ctx          context.Context
	accessToken  string
	refreshToken string
	mu           sync.Mutex
}

// newTokenManager parses credentials from config and ensures a valid token is available.
// It loads tokens.json if it exists and tries to refresh; otherwise it starts the
// one-time browser authorization flow.
func newTokenManager(ctx context.Context, uac usecases.ConfigurableAsset) (*TokenManager, error) {
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
	tm := &TokenManager{Credentials: creds, ctx: ctx}

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
	ctx := tm.ctx
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
	case <-ctx.Done():
		srv.Close()
		return fmt.Errorf("authorization canceled")
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
	// Netatmo signals an expired access token with 403 (error code 3), not 401.
	// Handling only 401 left the token un-refreshed after its ~3-hour life: the
	// 403 body — a JSON error envelope — then unmarshaled cleanly into an empty
	// device list, and the poll logged "data refreshed" over stale readings for
	// hours. Both 401 and 403 mean refresh. Anything else non-2xx is the
	// provider's answer and is returned as an error, never as a body to be
	// mistaken for data.
	if status == 401 || status == 403 {
		log.Printf("Netatmo: access token rejected (HTTP %d), refreshing...", status)
		if rerr := tm.refresh(); rerr != nil {
			log.Printf("Netatmo: refresh failed (%v), re-authorizing via browser...", rerr)
			if aerr := tm.authorizeWithBrowser(); aerr != nil {
				return nil, fmt.Errorf("re-authorization failed: %w", aerr)
			}
		}
		status, body, err = doGET(tm.getToken())
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("Netatmo returned HTTP %d: %s", status, strings.TrimSpace(string(body)))
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
		Mission:     components.MissionMeasurement,
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

// firstWait and maxWait bound the retry backoff. Variables rather than
// constants so a test can drive the loop without sleeping: the retry is what
// keeps the heating alive, and a behaviour that can only be tested by waiting
// five minutes is one nobody tests.
var (
	firstWait = 15 * time.Second
	maxWait   = 5 * time.Minute
)

// stationsWhenAvailable fetches the Netatmo station list, retrying until it
// answers with at least one device or the system is shut down.
//
// The backoff starts short and settles at five minutes, which is the station's
// own reporting period: asking faster than the data changes only spends the
// account's rate limit. The second return is false only when the context ended,
// so a caller can tell "shutting down" from "still trying".
func stationsWhenAvailable(sys *components.System, fetch func() (*StationsDataResponse, error)) (*StationsDataResponse, bool) {
	wait := firstWait

	for {
		stationData, err := fetch()
		switch {
		case err != nil:
			log.Printf("Netatmo: could not fetch station data (%v); retrying in %v\n", err, wait)
		case len(stationData.Body.Devices) == 0:
			log.Printf("Netatmo: the account reports no stations; retrying in %v\n", wait)
		default:
			return stationData, true
		}

		select {
		case <-time.After(wait):
		case <-sys.Ctx.Done():
			return nil, false
		}
		if wait *= 2; wait > maxWait {
			wait = maxWait
		}
	}
}

// -------------------------------------Asset instantiation entry point

// newResources is the single entry point called by main for this system.
// It authenticates with the Netatmo API, discovers all modules on the configured station,
// builds one UnitAsset per module, starts the background poller, and returns the assets.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	tm, err := newTokenManager(sys.Ctx, uac)
	if err != nil {
		log.Fatalf("Netatmo authentication failed: %v\n", err)
	}

	// The station list is fetched until it answers, not once.
	//
	// This used to be a pair of log.Fatal calls, and on 22 August the cottage
	// found out what that costs: the Netatmo API answered a restart with an
	// empty device list — transiently, seconds after a battery change — this
	// system exited, and the temperatures it provides went with it. The
	// ethermostat then had nothing to read, could not discover a single heater,
	// and the heating stopped being controlled at all while every other system
	// in the cloud reported itself healthy.
	//
	// Failing fast is right when the failure is local and permanent. This one is
	// neither: the reading comes from somebody else's web service over a
	// domestic internet connection, so "nothing right now" is the most ordinary
	// answer it can give. A system that keeps asking is noisy in the log; one
	// that exits is silent and takes the heating with it.
	stationData, ok := stationsWhenAvailable(sys, tm.fetchStationData)
	if !ok {
		return nil, func() {}
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
		details := map[string][]string{"Unit": {spec.unit}, "Forms": {"SignalA_v1a"}}
		if spec.quantityKind != "" {
			details["QuantityKind"] = []string{spec.quantityKind}
		}
		s := &components.Service{
			Definition:  spec.definition,
			SubPath:     spec.subPath,
			Details:     details,
			RegPeriod:   30,
			Description: spec.description,
		}
		services[spec.subPath] = s
	}

	// Secondary indoor modules (e.g. a room sensor named "Bathroom") publish their
	// ModuleName as FunctionalLocation so consumers can discover them by room name.
	functionalLocation := stationName
	if info.locationFromModuleName {
		functionalLocation = moduleName
	}

	ua := &components.UnitAsset{
		Name: info.assetName,
		// The taxonomy's value, not a phrase. This field carried
		// "provide_weather_data", which reads like a mission but is not one of
		// them, and the framework validates every constructed asset before the
		// system starts — so this system could not start at all. The template
		// that seeds the configuration file has always said measurement, which
		// is what these assets do: they report readings from a weather station.
		Mission: components.MissionMeasurement,
		Owner:   sys,
		Details: map[string][]string{
			"FunctionalLocation": {functionalLocation},
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

// staleAfter is how old the newest Netatmo reading may be before the poller
// warns that the station has gone quiet. Netatmo's modules report every few
// minutes; 30 minutes without a newer reading is the source, not the cadence.
const staleAfter = 30 * time.Minute

// -------------------------------------Background poller

// pollNetatmo refreshes the measurement cache on every tick until the context is canceled.
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
			updated := 0
			var newest time.Time
			for _, device := range resp.Body.Devices {
				if tm.StationName != "" && device.StationName != tm.StationName {
					continue
				}
				if info, ok := moduleTypeMap["NAMain"]; ok {
					ts := time.Unix(device.DashboardData.TimeUTC, 0)
					cache.update(info.assetName, extractMeasurements("NAMain", device.DashboardData), ts)
					updated++
					if ts.After(newest) {
						newest = ts
					}
				}
				for _, mod := range device.Modules {
					if info, ok := moduleTypeMap[mod.Type]; ok {
						ts := time.Unix(mod.DashboardData.TimeUTC, 0)
						cache.update(info.assetName, extractMeasurements(mod.Type, mod.DashboardData), ts)
						updated++
						if ts.After(newest) {
							newest = ts
						}
					}
				}
				break
			}
			// A reply that carried no station data is not a refresh. It happened
			// when a 403 unmarshaled to nothing; it should never again be logged
			// as success, because a flat line that reports itself healthy is the
			// most expensive kind.
			if updated == 0 {
				log.Println("Netatmo: the reply carried no station data — nothing updated")
				continue
			}
			// A log-only staleness warning: the readings are current but not
			// advancing. Netatmo's modules report every few minutes, so anything
			// older than staleAfter means the station itself has gone quiet — the
			// case that froze the cottage while every system looked well. Log
			// only: withholding a temperature from the control loop is the frost
			// guard's job, not this poller's.
			if age := time.Since(newest); age > staleAfter {
				log.Printf("Netatmo: WARNING — freshest reading is %v old; the station may have stopped reporting", age.Round(time.Minute))
			}
			log.Printf("Netatmo: %d module(s) refreshed", updated)
		}
	}
}
