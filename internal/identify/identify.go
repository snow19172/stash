// Package identify provides the scene identification functionality for the application.
// The identify functionality uses scene scrapers to identify a given scene and
// set its metadata based on the scraped data.
package identify

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/sliceutil"
	"github.com/stashapp/stash/pkg/txn"
	"github.com/stashapp/stash/pkg/utils"
)

var (
	ErrSkipSingleNamePerformer = errors.New("a performer was skipped because they only had a single name and no disambiguation")
)

type MultipleMatchesFoundError struct {
	Source ScraperSource
}

func (e *MultipleMatchesFoundError) Error() string {
	return fmt.Sprintf("multiple matches found for %s", e.Source.Name)
}

type SceneScraper interface {
	ScrapeScenes(ctx context.Context, sceneID int) ([]*models.ScrapedScene, error)
}

type SceneUpdatePostHookExecutor interface {
	ExecuteSceneUpdatePostHooks(ctx context.Context, input models.SceneUpdateInput, inputFields []string)
}

type ScraperSource struct {
	Name       string
	Options    *MetadataOptions
	Scraper    SceneScraper
	RemoteSite string
}

type SceneIdentifier struct {
	TxnManager         txn.Manager
	SceneReaderUpdater SceneReaderUpdater
	StudioReaderWriter models.StudioReaderWriter
	PerformerCreator   PerformerCreator
	TagFinderCreator   models.TagFinderCreator

	DefaultOptions              *MetadataOptions
	Sources                     []ScraperSource
	SceneUpdatePostHookExecutor SceneUpdatePostHookExecutor
	MergeSources                bool
	OnlyFillMissing             bool
}

func (t *SceneIdentifier) Identify(ctx context.Context, scene *models.Scene) error {
	result, err := t.scrapeScene(ctx, scene)
	var multipleMatchErr *MultipleMatchesFoundError
	if err != nil {
		if !errors.As(err, &multipleMatchErr) {
			return err
		}
	}

	if result == nil {
		if multipleMatchErr != nil {
			logger.Debugf("Identify skipped because multiple results returned for %s", scene.Path)

			// find if the scene should be tagged for multiple results
			options := t.getOptions(multipleMatchErr.Source)
			if options.SkipMultipleMatchTag != nil && len(*options.SkipMultipleMatchTag) > 0 {
				// Tag it with the multiple results tag
				err := t.addTagToScene(ctx, scene, *options.SkipMultipleMatchTag)
				if err != nil {
					return err
				}
				return nil
			}
		} else {
			logger.Debugf("Unable to identify %s", scene.Path)
		}
		return nil
	}

	// results were found, modify the scene
	if err := t.modifyScene(ctx, scene, result); err != nil {
		return fmt.Errorf("error modifying scene: %v", err)
	}

	return nil
}

type scrapeResult struct {
	result      *models.ScrapedScene
	source      ScraperSource
	sources     []ScraperSource
	fieldSource map[string]ScraperSource
}

func (t *SceneIdentifier) scrapeScene(ctx context.Context, scene *models.Scene) (*scrapeResult, error) {
	if t.MergeSources {
		var combined *scrapeResult
		var multipleMatchErr *MultipleMatchesFoundError
		for _, source := range t.Sources {
			results, err := source.Scraper.ScrapeScenes(ctx, scene.ID)
			if err != nil {
				logger.Errorf("error scraping from identification source %q: %v", source.Name, err)
				continue
			}
			logger.Debugf("Identification source %q returned %d results for %s", source.Name, len(results), scene.Path)
			if len(results) == 0 {
				continue
			}
			if results[0] == nil {
				continue
			}
			options := t.getOptions(source)
			if len(results) > 1 && utils.IsTrue(options.SkipMultipleMatches) {
				if multipleMatchErr == nil {
					multipleMatchErr = &MultipleMatchesFoundError{Source: source}
				}
				continue
			}
			scraped := ignoreScrapedSceneFields(results[0], source)
			if combined == nil {
				fieldSources := make(map[string]ScraperSource)
				for _, field := range scrapedSceneFields {
					if hasScrapedSceneField(scraped, field) {
						fieldSources[field] = source
					}
				}
				combined = &scrapeResult{
					result:      scraped,
					source:      source,
					sources:     []ScraperSource{source},
					fieldSource: fieldSources,
				}
				continue
			}
			combined.sources = append(combined.sources, source)
			if combined.fieldSource == nil {
				combined.fieldSource = make(map[string]ScraperSource)
			}
			missingFields := make(map[string]bool)
			for _, field := range scrapedSceneFields {
				missingFields[field] = !hasScrapedSceneField(combined.result, field) && hasScrapedSceneField(scraped, field)
			}
			mergeScrapedSceneFields(combined.result, scraped, combined.source.RemoteSite == source.RemoteSite)
			for field, wasMissing := range missingFields {
				if wasMissing && hasScrapedSceneField(combined.result, field) {
					combined.fieldSource[field] = source
				}
			}
		}
		if combined == nil {
			return nil, multipleMatchErr
		}
		return combined, nil
	}

	// iterate through the input sources
	for _, source := range t.Sources {
		// scrape using the source
		results, err := source.Scraper.ScrapeScenes(ctx, scene.ID)
		if err != nil {
			logger.Errorf("error scraping from identification source %q: %v", source.Name, err)
			continue
		}
		logger.Debugf("Identification source %q returned %d results for %s", source.Name, len(results), scene.Path)

		if len(results) > 0 {
			options := t.getOptions(source)
			if len(results) > 1 && utils.IsTrue(options.SkipMultipleMatches) {
				return nil, &MultipleMatchesFoundError{
					Source: source,
				}
			} else {
				// if results were found then return
				return &scrapeResult{
					result:      results[0],
					source:      source,
					sources:     []ScraperSource{source},
					fieldSource: scrapedSceneFieldSources(results[0], source),
				}, nil
			}
		}
	}

	return nil, nil
}

var scrapedSceneFields = []string{
	"title", "code", "date", "production_date", "director", "details",
	"cover_image", "url", "studio", "performers", "tags", "stash_ids",
}

func scrapedSceneFieldSources(scraped *models.ScrapedScene, source ScraperSource) map[string]ScraperSource {
	ret := make(map[string]ScraperSource)
	for _, field := range scrapedSceneFields {
		if hasScrapedSceneField(scraped, field) {
			ret[field] = source
		}
	}
	return ret
}

func hasScrapedSceneField(scraped *models.ScrapedScene, field string) bool {
	if scraped == nil {
		return false
	}
	switch field {
	case "title":
		return scraped.Title != nil && *scraped.Title != ""
	case "code":
		return scraped.Code != nil && *scraped.Code != ""
	case "date":
		return scraped.Date != nil && *scraped.Date != ""
	case "production_date":
		return scraped.ProductionDate != nil && *scraped.ProductionDate != ""
	case "director":
		return scraped.Director != nil && *scraped.Director != ""
	case "details":
		return scraped.Details != nil && *scraped.Details != ""
	case "cover_image":
		return scraped.Image != nil && *scraped.Image != ""
	case "url":
		return (scraped.URL != nil && *scraped.URL != "") || len(scraped.URLs) > 0
	case "studio":
		return scraped.Studio != nil
	case "performers":
		return len(scraped.Performers) > 0
	case "tags":
		return len(scraped.Tags) > 0
	case "stash_ids":
		return scraped.RemoteSiteID != nil
	default:
		return false
	}
}

func ignoreScrapedSceneFields(scraped *models.ScrapedScene, source ScraperSource) *models.ScrapedScene {
	if source.Options == nil {
		return scraped
	}

	ret := *scraped
	for _, fieldOption := range source.Options.FieldOptions {
		if fieldOption == nil || fieldOption.Strategy != FieldStrategyIgnore {
			continue
		}
		switch fieldOption.Field {
		case "title":
			ret.Title = nil
		case "code":
			ret.Code = nil
		case "date":
			ret.Date = nil
		case "production_date":
			ret.ProductionDate = nil
		case "director":
			ret.Director = nil
		case "details":
			ret.Details = nil
		case "cover_image":
			ret.Image = nil
		case "url":
			ret.URL = nil
			ret.URLs = nil
		case "studio":
			ret.Studio = nil
		case "performers":
			ret.Performers = nil
		case "tags":
			ret.Tags = nil
		case "stash_ids":
			ret.RemoteSiteID = nil
		}
	}
	return &ret
}

func mergeScrapedSceneFields(target, source *models.ScrapedScene, mergeRelationships bool) {
	if target.Title == nil || *target.Title == "" {
		target.Title = source.Title
	}
	if target.Code == nil || *target.Code == "" {
		target.Code = source.Code
	}
	if target.Details == nil || *target.Details == "" {
		target.Details = source.Details
	}
	if target.Director == nil || *target.Director == "" {
		target.Director = source.Director
	}
	if target.URL == nil || *target.URL == "" {
		target.URL = source.URL
	}
	if len(target.URLs) == 0 {
		target.URLs = source.URLs
	}
	if target.Date == nil || *target.Date == "" {
		target.Date = source.Date
	}
	if target.ProductionDate == nil || *target.ProductionDate == "" {
		target.ProductionDate = source.ProductionDate
	}
	if target.Duration == nil {
		target.Duration = source.Duration
	}
	if target.Image == nil || *target.Image == "" {
		target.Image = source.Image
	}
	if !mergeRelationships {
		return
	}
	if target.Studio == nil && (mergeRelationships || source.Studio == nil || source.Studio.StoredID != nil) {
		target.Studio = source.Studio
	}
	if len(target.Tags) == 0 && (mergeRelationships || hasStoredTagIDs(source.Tags)) {
		target.Tags = source.Tags
	}
	if len(target.Performers) == 0 && (mergeRelationships || hasStoredPerformerIDs(source.Performers)) {
		target.Performers = source.Performers
	}
	if len(target.Groups) == 0 && mergeRelationships {
		target.Groups = source.Groups
	}
	if len(target.Movies) == 0 && mergeRelationships {
		target.Movies = source.Movies
	}
	if target.RemoteSiteID == nil && mergeRelationships {
		target.RemoteSiteID = source.RemoteSiteID
	}
	if target.File == nil && mergeRelationships {
		target.File = source.File
	}
	if len(target.Fingerprints) == 0 && mergeRelationships {
		target.Fingerprints = source.Fingerprints
	}
}

func hasStoredTagIDs(tags []*models.ScrapedTag) bool {
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if tag == nil || tag.StoredID == nil {
			return false
		}
	}
	return true
}

func hasStoredPerformerIDs(performers []*models.ScrapedPerformer) bool {
	if len(performers) == 0 {
		return false
	}
	for _, performer := range performers {
		if performer == nil || performer.StoredID == nil {
			return false
		}
	}
	return true
}

// Returns a MetadataOptions object with any default options overwritten by source specific options
func (t *SceneIdentifier) getOptions(source ScraperSource) MetadataOptions {
	var options MetadataOptions
	if t.DefaultOptions != nil {
		options = *t.DefaultOptions
	}
	if source.Options == nil {
		return options
	}

	if source.Options.SetCoverImage != nil {
		options.SetCoverImage = source.Options.SetCoverImage
	}
	if source.Options.SetOrganized != nil {
		options.SetOrganized = source.Options.SetOrganized
	}
	if source.Options.IncludeMalePerformers != nil {
		options.IncludeMalePerformers = source.Options.IncludeMalePerformers
	}
	if len(source.Options.PerformerGenders) > 0 {
		options.PerformerGenders = source.Options.PerformerGenders
	}
	if source.Options.SkipMultipleMatches != nil {
		options.SkipMultipleMatches = source.Options.SkipMultipleMatches
	}
	if source.Options.SkipMultipleMatchTag != nil && len(*source.Options.SkipMultipleMatchTag) > 0 {
		options.SkipMultipleMatchTag = source.Options.SkipMultipleMatchTag
	}
	if source.Options.SkipSingleNamePerformers != nil {
		options.SkipSingleNamePerformers = source.Options.SkipSingleNamePerformers
	}
	if source.Options.SkipSingleNamePerformerTag != nil && len(*source.Options.SkipSingleNamePerformerTag) > 0 {
		options.SkipSingleNamePerformerTag = source.Options.SkipSingleNamePerformerTag
	}

	return options
}

func (t *SceneIdentifier) getSceneUpdater(ctx context.Context, s *models.Scene, result *scrapeResult) (*scene.UpdateSet, error) {
	ret := &scene.UpdateSet{
		ID: s.ID,
	}

	fieldOptions := t.getResultFieldOptions(result)
	options := t.getOptions(result.source)
	performerOptions := t.getOptions(t.sourceForField(result, "performers"))
	coverOptions := t.getOptions(t.sourceForField(result, "cover_image"))

	scraped := result.result

	rel := sceneRelationships{
		sceneReader:              t.SceneReaderUpdater,
		studioReaderWriter:       t.StudioReaderWriter,
		performerCreator:         t.PerformerCreator,
		tagCreator:               t.TagFinderCreator,
		scene:                    s,
		result:                   result,
		fieldOptions:             fieldOptions,
		skipSingleNamePerformers: utils.IsTrue(performerOptions.SkipSingleNamePerformers),
		fillMissing:              t.OnlyFillMissing,
	}

	setOrganized := utils.IsTrue(options.SetOrganized)
	ret.Partial = getScenePartial(s, scraped, fieldOptions, setOrganized)

	studioID, err := rel.studio(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting studio: %w", err)
	}

	if studioID != nil {
		ret.Partial.StudioID = models.NewOptionalInt(*studioID)
	}

	// Determine allowed genders for performer filtering
	var allowedGenders []models.GenderEnum
	if len(performerOptions.PerformerGenders) > 0 {
		// New field takes precedence
		allowedGenders = performerOptions.PerformerGenders
	} else if performerOptions.IncludeMalePerformers != nil && !*performerOptions.IncludeMalePerformers {
		// Legacy: if includeMalePerformers is false, include all genders except male
		for _, g := range models.AllGenderEnum {
			if g != models.GenderEnumMale {
				allowedGenders = append(allowedGenders, g)
			}
		}
	}
	// nil allowedGenders means include all performers

	addSkipSingleNamePerformerTag := false
	performerIDs, err := rel.performers(ctx, allowedGenders)
	if err != nil {
		if errors.Is(err, ErrSkipSingleNamePerformer) {
			addSkipSingleNamePerformerTag = true
		} else {
			return nil, err
		}
	}
	if performerIDs != nil {
		ret.Partial.PerformerIDs = &models.UpdateIDs{
			IDs:  performerIDs,
			Mode: models.RelationshipUpdateModeSet,
		}
	}

	tagIDs, err := rel.tags(ctx)
	if err != nil {
		return nil, err
	}
	if addSkipSingleNamePerformerTag && performerOptions.SkipSingleNamePerformerTag != nil {
		tagID, err := strconv.ParseInt(*performerOptions.SkipSingleNamePerformerTag, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("error converting tag ID %s: %w", *performerOptions.SkipSingleNamePerformerTag, err)
		}

		tagIDs = sliceutil.AppendUnique(tagIDs, int(tagID))
	}
	if tagIDs != nil {
		ret.Partial.TagIDs = &models.UpdateIDs{
			IDs:  tagIDs,
			Mode: models.RelationshipUpdateModeSet,
		}
	}

	// SetCoverImage defaults to true if unset
	if coverOptions.SetCoverImage == nil || *coverOptions.SetCoverImage {
		ret.CoverImage, err = rel.cover(ctx)
		if err != nil {
			return nil, err
		}
	}

	// if anything changed, also update the updated at time on the applicable stash id
	changed := !ret.IsEmpty()

	stashIDs, err := rel.stashIDs(ctx, changed)
	if err != nil {
		return nil, err
	}
	if stashIDs != nil {
		ret.Partial.StashIDs = &models.UpdateStashIDs{
			StashIDs: stashIDs,
			Mode:     models.RelationshipUpdateModeSet,
		}
	}

	return ret, nil
}

func (t *SceneIdentifier) getResultFieldOptions(result *scrapeResult) map[string]*FieldOptions {
	if len(result.fieldSource) > 0 {
		ret := make(map[string]*FieldOptions, len(result.fieldSource))
		for field, source := range result.fieldSource {
			sourceOptions := []MetadataOptions{}
			if source.Options != nil {
				sourceOptions = append(sourceOptions, *source.Options)
			}
			if t.DefaultOptions != nil {
				sourceOptions = append(sourceOptions, *t.DefaultOptions)
			}
			if option := getFieldOptions(sourceOptions)[field]; option != nil {
				ret[field] = option
			}
		}
		return ret
	}

	allOptions := []MetadataOptions{}
	for _, source := range result.sources {
		if source.Options != nil {
			allOptions = append(allOptions, *source.Options)
		}
	}
	if len(allOptions) == 0 && result.source.Options != nil {
		allOptions = append(allOptions, *result.source.Options)
	}
	if t.DefaultOptions != nil {
		allOptions = append(allOptions, *t.DefaultOptions)
	}

	return getFieldOptions(allOptions)
}

func (t *SceneIdentifier) sourceForField(result *scrapeResult, field string) ScraperSource {
	if source, ok := result.fieldSource[field]; ok {
		return source
	}
	return result.source
}

func (t *SceneIdentifier) modifyScene(ctx context.Context, s *models.Scene, result *scrapeResult) error {
	var updater *scene.UpdateSet
	if err := txn.WithTxn(ctx, t.TxnManager, func(ctx context.Context) error {
		// load scene relationships
		if err := s.LoadURLs(ctx, t.SceneReaderUpdater); err != nil {
			return err
		}
		if err := s.LoadPerformerIDs(ctx, t.SceneReaderUpdater); err != nil {
			return err
		}
		if err := s.LoadTagIDs(ctx, t.SceneReaderUpdater); err != nil {
			return err
		}
		if err := s.LoadStashIDs(ctx, t.SceneReaderUpdater); err != nil {
			return err
		}

		var err error
		updater, err = t.getSceneUpdater(ctx, s, result)
		if err != nil {
			return err
		}

		// don't update anything if nothing was set
		if updater.IsEmpty() {
			logger.Debugf("Nothing to set for %s", s.Path)
			return nil
		}

		if _, err := updater.Update(ctx, t.SceneReaderUpdater); err != nil {
			return fmt.Errorf("error updating scene: %w", err)
		}

		as := ""
		title := updater.Partial.Title
		if title.Ptr() != nil {
			as = fmt.Sprintf(" as %s", title.Value)
		}
		sourceNames := make([]string, 0, len(result.sources))
		for _, source := range result.sources {
			sourceNames = append(sourceNames, source.Name)
		}
		if len(sourceNames) == 0 {
			sourceNames = append(sourceNames, result.source.Name)
		}
		logger.Infof("Successfully identified %s%s using %s", s.Path, as, strings.Join(sourceNames, ", "))

		return nil
	}); err != nil {
		return err
	}

	// fire post-update hooks
	if !updater.IsEmpty() {
		updateInput := updater.UpdateInput()
		fields := utils.NotNilFields(updateInput, "json")
		t.SceneUpdatePostHookExecutor.ExecuteSceneUpdatePostHooks(ctx, updateInput, fields)
	}

	return nil
}

func (t *SceneIdentifier) addTagToScene(ctx context.Context, s *models.Scene, tagToAdd string) error {
	if err := txn.WithTxn(ctx, t.TxnManager, func(ctx context.Context) error {
		tagID, err := strconv.Atoi(tagToAdd)
		if err != nil {
			return fmt.Errorf("error converting tag ID %s: %w", tagToAdd, err)
		}

		if err := s.LoadTagIDs(ctx, t.SceneReaderUpdater); err != nil {
			return err
		}
		existing := s.TagIDs.List()

		if slices.Contains(existing, tagID) {
			// skip if the scene was already tagged
			return nil
		}

		if err := scene.AddTag(ctx, t.SceneReaderUpdater, s, tagID); err != nil {
			return err
		}

		ret, err := t.TagFinderCreator.Find(ctx, tagID)
		if err != nil {
			logger.Infof("Added tag id %s to skipped scene %s", tagToAdd, s.Path)
		} else {
			logger.Infof("Added tag %s to skipped scene %s", ret.Name, s.Path)
		}

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func getFieldOptions(options []MetadataOptions) map[string]*FieldOptions {
	// prefer source-specific field strategies, then the defaults
	ret := make(map[string]*FieldOptions)
	for _, oo := range options {
		for _, f := range oo.FieldOptions {
			if _, found := ret[f.Field]; !found {
				ret[f.Field] = f
			}
		}
	}

	return ret
}

// setPartialDate sets target from the scraped date value, if the field strategy
// allows it and the scraped value differs from the existing one. fieldName is
// used for logging only. Parse failures are logged and target is left unset,
// rather than failing the whole identify operation over one bad field.
func setPartialDate(target *models.OptionalDate, scrapedDate *string, existing *models.Date, options *FieldOptions, fieldName string) {
	if scrapedDate == nil || (existing != nil && existing.String() == *scrapedDate) {
		return
	}

	if !shouldSetSingleValueField(options, existing != nil) {
		return
	}

	d, err := models.ParseDate(*scrapedDate)
	if err != nil {
		logger.Warnf("Ignoring scraped %s %q: %v", fieldName, *scrapedDate, err)
		return
	}

	*target = models.NewOptionalDate(d)
}

func getScenePartial(scene *models.Scene, scraped *models.ScrapedScene, fieldOptions map[string]*FieldOptions, setOrganized bool) models.ScenePartial {
	partial := models.ScenePartial{}

	if scraped.Title != nil && (scene.Title != *scraped.Title) {
		if shouldSetSingleValueField(fieldOptions["title"], scene.Title != "") {
			partial.Title = models.NewOptionalString(*scraped.Title)
		}
	}
	setPartialDate(&partial.Date, scraped.Date, scene.Date, fieldOptions["date"], "date")
	setPartialDate(&partial.ProductionDate, scraped.ProductionDate, scene.ProductionDate, fieldOptions["production_date"], "production date")
	if scraped.Details != nil && (scene.Details != *scraped.Details) {
		if shouldSetSingleValueField(fieldOptions["details"], scene.Details != "") {
			partial.Details = models.NewOptionalString(*scraped.Details)
		}
	}
	if len(scraped.URLs) > 0 && shouldSetSingleValueField(fieldOptions["url"], false) {
		// if overwrite, then set over the top
		switch getFieldStrategy(fieldOptions["url"]) {
		case FieldStrategyOverwrite:
			// only overwrite if not equal
			if len(sliceutil.Exclude(scraped.URLs, scene.URLs.List())) != 0 {
				partial.URLs = &models.UpdateStrings{
					Values: scraped.URLs,
					Mode:   models.RelationshipUpdateModeSet,
				}
			}
		case FieldStrategyMerge:
			// if merge, add if not already present
			urls := sliceutil.AppendUniques(scene.URLs.List(), scraped.URLs)

			if len(urls) != len(scene.URLs.List()) {
				partial.URLs = &models.UpdateStrings{
					Values: urls,
					Mode:   models.RelationshipUpdateModeSet,
				}
			}
		}
	}
	if scraped.Director != nil && (scene.Director != *scraped.Director) {
		if shouldSetSingleValueField(fieldOptions["director"], scene.Director != "") {
			partial.Director = models.NewOptionalString(*scraped.Director)
		}
	}
	if scraped.Code != nil && (scene.Code != *scraped.Code) {
		if shouldSetSingleValueField(fieldOptions["code"], scene.Code != "") {
			partial.Code = models.NewOptionalString(*scraped.Code)
		}
	}

	if setOrganized && !scene.Organized {
		partial.Organized = models.NewOptionalBool(true)
	}

	return partial
}

func getFieldStrategy(strategy *FieldOptions) FieldStrategy {
	// if unset then default to MERGE
	fs := FieldStrategyMerge

	if strategy != nil && strategy.Strategy.IsValid() {
		fs = strategy.Strategy
	}

	return fs
}

func shouldSetSingleValueField(strategy *FieldOptions, hasExistingValue bool) bool {
	// if unset then default to MERGE
	fs := getFieldStrategy(strategy)

	if fs == FieldStrategyIgnore {
		return false
	}

	return !hasExistingValue || fs == FieldStrategyOverwrite
}
