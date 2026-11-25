package config

import (
	"testing"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stretchr/testify/assert"
)

func TestConfig_GetAllPluginConfiguration(t *testing.T) {
	i := InitializeEmpty()

	assert.Equal(t, i.GetAllPluginConfiguration(), map[string]map[string]interface{}{})

	i.SetPluginConfiguration("plugin1", map[string]interface{}{"key1": "value1"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
	}, i.GetAllPluginConfiguration())

	i.SetPluginConfiguration("plugin2", map[string]interface{}{"key2": "value2"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
		"plugin2": {"key2": "value2"},
	}, i.GetAllPluginConfiguration())

	// ensure SetPluginConfiguration overwrites existing configuration
	i.SetPluginConfiguration("plugin2", map[string]interface{}{"key3": "value3"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
		"plugin2": {"key3": "value3"},
	}, i.GetAllPluginConfiguration())
}

func TestConfig_initialisePublicWhitelist(t *testing.T) {
	i := InitializeEmpty()
	i.SetInterface(PublicWhitelist, []string{"203.0.113.45", "198.51.100.0/24", "2001:db8::/32"})

	assert.NoError(t, i.initialisePublicWhitelist())
	nets, addrs := i.GetPublicWhitelist()
	assert.Len(t, nets, 2)
	assert.Len(t, addrs, 1)
	assert.Equal(t, "203.0.113.45", addrs[0].String())

	for _, bad := range []string{"/0", "not-an-ip", "*"} {
		i := InitializeEmpty()
		i.SetInterface(PublicWhitelist, []string{bad})
		assert.Error(t, i.initialisePublicWhitelist(), bad)
	}
}

func TestConfig_migrateLegacyAutoIdentifySettings(t *testing.T) {
	i := InitializeEmpty()
	scraperID := "test-scraper"
	i.SetInterface(Stash, StashConfigs{{Path: "/existing"}})
	i.SetInterface(AutoIdentifyLibraries, []*AutoIdentifyLibraryConfig{
		{
			Path: "/existing",
			Sources: []*identify.Source{
				{Source: &scraper.Source{ScraperID: &scraperID}},
			},
		},
	})
	i.SetBool("auto_watch", true)
	i.SetBool("auto_identify", true)
	i.SetBool("auto_identify_overwrite", true)

	i.migrateLegacyAutoIdentifySettings()

	libraries := i.GetAutoIdentifyLibraries()
	assert.Len(t, libraries, 1)
	assert.True(t, libraries[0].AutoScan)
	assert.True(t, libraries[0].AutoIdentify)
	assert.False(t, libraries[0].AutoIdentifyOverwrite)
	assert.Equal(t, identify.FieldStrategyOverwrite, libraries[0].Sources[0].Options.FieldOptions[0].Strategy)
	assert.False(t, i.getBool("auto_watch"))
	assert.False(t, i.getBool("auto_identify"))
	assert.False(t, i.getBool("auto_identify_overwrite"))

	i.SetInterface(Stash, StashConfigs{
		{Path: "/existing"},
		{Path: "/new"},
	})
	i.migrateLegacyAutoIdentifySettings()
	assert.Len(t, i.GetAutoIdentifyLibraries(), 1)
}
