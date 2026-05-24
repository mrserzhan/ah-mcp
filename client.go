package main

import (
	"fmt"
	"sync"

	appie "github.com/gwillem/appie-go"
)

var (
	clientMu     sync.RWMutex
	globalClient *appie.Client
)

// GetClient returns the singleton appie client, creating it from the tokens file
// if it has not been initialised yet.
func GetClient() (*appie.Client, error) {
	clientMu.RLock()
	c := globalClient
	clientMu.RUnlock()
	if c != nil {
		return c, nil
	}
	return ReloadClient()
}

// ReloadClient creates a fresh appie client loaded from the tokens file.
// Call this after a successful OAuth login to pick up newly saved tokens.
func ReloadClient() (*appie.Client, error) {
	path := TokensPath()
	c, err := appie.NewWithConfig(path, appie.WithSite(ahSite()))
	if err != nil {
		return nil, fmt.Errorf("create appie client: %w", err)
	}
	clientMu.Lock()
	globalClient = c
	clientMu.Unlock()
	return c, nil
}
