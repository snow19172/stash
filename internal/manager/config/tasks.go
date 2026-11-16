package config

import "github.com/stashapp/stash/internal/identify"

type ScanMetadataOptions struct {
	// Forces a rescan on files even if they have not changed
	Rescan bool `json:"rescan"`
	// Generate scene covers during scan
	ScanGenerateCovers bool `json:"scanGenerateCovers"`
	// Generate previews during scan
	ScanGeneratePreviews bool `json:"scanGeneratePreviews"`
	// Generate image previews during scan
	ScanGenerateImagePreviews bool `json:"scanGenerateImagePreviews"`
	// Generate sprites during scan
	ScanGenerateSprites bool `json:"scanGenerateSprites"`
	// Generate video phashes during scan
	ScanGeneratePhashes bool `json:"scanGeneratePhashes"`
	// Generate image phashes during scan
	ScanGenerateImagePhashes bool `json:"scanGenerateImagePhashes"`
	// Generate image thumbnails during scan
	ScanGenerateThumbnails bool `json:"scanGenerateThumbnails"`
	// Generate image thumbnails during scan
	ScanGenerateClipPreviews bool `json:"scanGenerateClipPreviews"`
}

type AutoTagMetadataOptions struct {
	// IDs of performers to tag files with, or "*" for all
	Performers []string `json:"performers"`
	// IDs of studios to tag files with, or "*" for all
	Studios []string `json:"studios"`
	// IDs of tags to tag files with, or "*" for all
	Tags []string `json:"tags"`
}

type AutoIdentifyLibraryConfig struct {
	Path                  string             `json:"path"`
	AutoScan              bool               `json:"autoScan"`
	AutoIdentify          bool               `json:"autoIdentify"`
	IgnoreOrganizedScenes bool               `json:"ignoreOrganizedScenes"`
	AutoIdentifyOverwrite bool               `json:"autoIdentifyOverwrite,omitempty"`
	Sources               []*identify.Source `json:"sources"`
}

func migrateAutoIdentifyOverwrite(libraries []*AutoIdentifyLibraryConfig) {
	fields := []string{
		"title", "code", "date", "production_date", "director", "details",
		"url", "cover_image", "studio", "performers", "tags", "stash_ids",
	}

	for _, library := range libraries {
		if library == nil || !library.AutoIdentifyOverwrite {
			continue
		}
		for _, source := range library.Sources {
			if source == nil {
				continue
			}
			if source.Options == nil {
				source.Options = &identify.MetadataOptions{}
			}

			fieldOptions := make(map[string]*identify.FieldOptions, len(fields))
			for _, option := range source.Options.FieldOptions {
				if option != nil {
					fieldOptions[option.Field] = option
				}
			}
			for _, field := range fields {
				option := fieldOptions[field]
				if option == nil {
					option = &identify.FieldOptions{Field: field}
					source.Options.FieldOptions = append(source.Options.FieldOptions, option)
				}
				option.Strategy = identify.FieldStrategyOverwrite
			}
		}
		library.AutoIdentifyOverwrite = false
	}
}
