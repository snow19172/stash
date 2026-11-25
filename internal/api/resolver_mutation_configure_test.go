package api

import (
	"testing"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stretchr/testify/require"
)

func TestValidateAutoIdentifyLibraries(t *testing.T) {
	scraperID := "scene-scraper"
	endpoint := "https://stash-box.example/graphql"

	tests := []struct {
		name      string
		libraries []*config.AutoIdentifyLibraryConfig
		wantError bool
	}{
		{
			name: "ordered scraper and Stash-Box sources",
			libraries: []*config.AutoIdentifyLibraryConfig{
				{
					Path: "/library",
					Sources: []*identify.Source{
						{Source: &scraper.Source{ScraperID: &scraperID}},
						{Source: &scraper.Source{StashBoxEndpoint: &endpoint}},
					},
				},
			},
		},
		{
			name: "source must be configured",
			libraries: []*config.AutoIdentifyLibraryConfig{
				{Path: "/library", Sources: []*identify.Source{{}}},
			},
			wantError: true,
		},
		{
			name: "source must have exactly one type",
			libraries: []*config.AutoIdentifyLibraryConfig{
				{
					Path: "/library",
					Sources: []*identify.Source{
						{Source: &scraper.Source{ScraperID: &scraperID, StashBoxEndpoint: &endpoint}},
					},
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAutoIdentifyLibraries(tt.libraries)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAutoScanConfigurationChanged(t *testing.T) {
	tests := []struct {
		name string
		old  []*config.AutoIdentifyLibraryConfig
		next []*config.AutoIdentifyLibraryConfig
		want bool
	}{
		{
			name: "identification-only settings do not refresh watcher",
			old:  []*config.AutoIdentifyLibraryConfig{{Path: "/library"}},
			next: []*config.AutoIdentifyLibraryConfig{{
				Path:         "/library",
				AutoIdentify: true,
			}},
			want: false,
		},
		{
			name: "enabling scan refreshes watcher",
			old:  []*config.AutoIdentifyLibraryConfig{{Path: "/library"}},
			next: []*config.AutoIdentifyLibraryConfig{{
				Path:     "/library",
				AutoScan: true,
			}},
			want: true,
		},
		{
			name: "disabling scan refreshes watcher",
			old:  []*config.AutoIdentifyLibraryConfig{{Path: "/library", AutoScan: true}},
			next: []*config.AutoIdentifyLibraryConfig{{Path: "/library"}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoScanConfigurationChanged(tt.old, tt.next); got != tt.want {
				t.Fatalf("autoScanConfigurationChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}
